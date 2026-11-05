// Package ha 实现控制台主备高可用（规格书 P9）：多台控制台共用同一个 PostgreSQL，
// 用 PostgreSQL 会话级 advisory lock 选主。只有主控制台接受节点连接、提供 Web 控制台并运行后台任务
// （证书续期、告警评估、数据清理）；备控制台对外只返回 503 与 /healthz，keepalived 据此把 VIP 漂到主控制台。
//
// 锁跟随数据库会话：主控制台进程退出或与数据库断开，会话结束即释放锁，备控制台在下一个轮询周期接管。
// 主控制台每个周期在持锁的连接上探活，探活失败立即降级（取消后台任务、断开节点），因此同时存在两个主的时间不超过一个周期。
package ha

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// LockKey 是控制台选主使用的 advisory lock 键（"ETOSHA01" 的 ASCII）。
const LockKey int64 = 0x45544f5348413031

// Elector 负责选主。DB 为 nil 时（SQLite 或未开启高可用）本进程始终是主。
type Elector struct {
	DB       *sql.DB
	Interval time.Duration // 轮询与探活周期，默认 3 秒
	// Licensed 返回是否有 ha 授权。未授权时，从未当过主的进程只在启动时尝试一次取锁（备控制台不会自动接管）；
	// 当过主的进程（例如数据库短暂中断后）仍会重新取锁。任何情况下都不会出现两个主。
	Licensed func() bool
	Log      *slog.Logger

	leader atomic.Bool
	since  atomic.Int64
}

// Enabled 报告是否以高可用模式运行。
func (e *Elector) Enabled() bool { return e.DB != nil }

// IsLeader 报告本进程当前是否为主。
func (e *Elector) IsLeader() bool { return e.leader.Load() }

// Since 返回成为主的时间（不是主时为零值）。
func (e *Elector) Since() time.Time {
	if v := e.since.Load(); v != 0 && e.IsLeader() {
		return time.Unix(0, v)
	}
	return time.Time{}
}

// Run 阻塞到 ctx 结束。每次成为主时同步调用 lead(lctx)：lead 应完成接管准备并启动后台任务后立即返回，
// 返回之后本进程才对外表现为主；失去主身份时取消 lctx，并调用 resign（例如断开所有节点连接）。
func (e *Elector) Run(ctx context.Context, lead func(context.Context), resign func()) {
	if e.DB == nil {
		lead(ctx)
		e.become(true)
		<-ctx.Done()
		return
	}
	iv := e.Interval
	if iv <= 0 {
		iv = 3 * time.Second
	}
	tried, led := false, false
	for {
		if !tried || led || e.Licensed == nil || e.Licensed() {
			tried = true
			if conn, ok := e.tryLock(ctx); ok {
				led = true
				e.hold(ctx, conn, iv, lead, resign)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(iv):
		}
	}
}

func (e *Elector) tryLock(ctx context.Context) (*sql.Conn, bool) {
	conn, err := e.DB.Conn(ctx)
	if err != nil {
		e.warn("ha: database unavailable", err)
		return nil, false
	}
	// 主机断电或断网时不会发 FIN：数据库靠 TCP 探测发现持锁会话已死才会释放锁，默认要 2 小时。
	// 在这条会话上收紧到约 11 秒（空闲 5 秒后每 2 秒探测、3 次无应答即断开；有未确认数据时 10 秒超时），
	// 加上备控制台的轮询周期，接管时间远小于规格书要求的 30 秒。旧主自己的探活（每个周期一次，超时一个周期）
	// 先于数据库发现断连并降级，所以不会出现两个主。Unix 套接字连接上这些设置不适用，失败时忽略。
	for _, s := range []string{
		"SET tcp_keepalives_idle = 5", "SET tcp_keepalives_interval = 2",
		"SET tcp_keepalives_count = 3", "SET tcp_user_timeout = 10000",
	} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			e.warn("ha: "+s, err)
		}
	}
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", LockKey).Scan(&got); err != nil || !got {
		if err != nil {
			e.warn("ha: try lock", err)
		}
		_ = conn.Close()
		return nil, false
	}
	return conn, true
}

// hold 在持锁期间探活，直到探活失败或 ctx 结束。
func (e *Elector) hold(ctx context.Context, conn *sql.Conn, iv time.Duration, lead func(context.Context), resign func()) {
	lctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); lead(lctx) }()
	wg.Wait()
	e.become(true)
	if e.Log != nil {
		e.Log.Info("ha: this console is now the leader")
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
		case <-t.C:
			pctx, pc := context.WithTimeout(ctx, iv)
			err := conn.PingContext(pctx)
			pc()
			if err == nil {
				continue
			}
			e.warn("ha: lost the database session, stepping down", err)
		}
		break
	}
	e.become(false)
	cancel()
	if resign != nil {
		resign()
	}
	// 主动释放锁，再丢弃这条连接（不放回连接池），确保会话结束、锁不会残留在池中的连接上
	uctx, uc := context.WithTimeout(context.Background(), time.Second)
	_, _ = conn.ExecContext(uctx, "SELECT pg_advisory_unlock($1)", LockKey)
	uc()
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func (e *Elector) become(v bool) {
	if v {
		e.since.Store(time.Now().UnixNano())
	} else {
		e.since.Store(0)
	}
	e.leader.Store(v)
}

func (e *Elector) warn(msg string, err error) {
	if e.Log != nil {
		e.Log.Warn(msg, "err", err)
	}
}
