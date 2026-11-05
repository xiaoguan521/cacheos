package instance

// 用真实的 sycache-server 验证实例生命周期与参数状态机。
// 运行：SYCACHE_BIN=<.../opt/sycache/bin> go test ./internal/agent/instance/ -run Integration -v
// 未设置 SYCACHE_BIN 时跳过（CI 的 make agent-it 会设置）。

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/resp"
)

// fakeWrapper 代替 sudo + sycache-wrapper + systemd：直接以后台方式启动 / 停止 sycache-server。
func fakeWrapper(t *testing.T, bin string, l Layout) Runner {
	pid := func(port string) int {
		b, _ := os.ReadFile(filepath.Join(l.RunDir, "sycache-"+port+".pid"))
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	stop := func(port string) {
		if p := pid(port); p > 0 {
			_ = syscall.Kill(p, syscall.SIGTERM)
			for i := 0; i < 100 && syscall.Kill(p, 0) == nil; i++ {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	start := func(port string) (string, error) {
		conf := filepath.Join(l.Root, "instances", port, "sycache.conf")
		out, err := exec.Command(filepath.Join(bin, "sycache-server"), conf, "--daemonize", "yes").CombinedOutput()
		return string(out), err
	}
	return func(ctx context.Context, args ...string) (string, error) {
		switch args[0] {
		case "start":
			return start(args[1])
		case "stop":
			stop(args[1])
			return "", nil
		case "restart":
			stop(args[1])
			return start(args[1])
		}
		return "", fmt.Errorf("unexpected wrapper call %q", args)
	}
}

func freePort(t *testing.T) uint32 {
	for {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if p >= 1024 && p < 60000 {
			return uint32(p)
		}
	}
}

func TestIntegrationLifecycleAndParams(t *testing.T) {
	bin := os.Getenv("SYCACHE_BIN")
	if bin == "" {
		t.Skip("SYCACHE_BIN not set")
	}
	base, err := os.MkdirTemp("/tmp", "sc")
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Root: base + "/opt", RunDir: base + "/run", LogDir: base + "/log", StateDir: base + "/state"}
	for _, d := range []string{l.Root + "/instances", l.RunDir, l.LogDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	m := &Manager{L: l}
	m.Run = fakeWrapper(t, bin, l)
	ctx := context.Background()
	master, replica := freePort(t), freePort(t)
	t.Cleanup(func() {
		for _, p := range []uint32{master, replica} {
			_, _ = m.Run(ctx, "stop", strconv.Itoa(int(p)))
		}
		os.RemoveAll(base)
	})
	h := sha("app-pass")
	base0 := map[string]string{"maxmemory": "64mb", "maxmemory-policy": "allkeys-lru", "save": "", "appendonly": "no"}
	s, err := m.Create(ctx, &agentv1.CreateInstance{Port: master, Params: base0, RevisionId: "p00000001", Masterauth: "repl-secret",
		AclUsers: []*agentv1.AclUser{{Name: "app", PasswordSha256: []string{h}, Rules: []string{"~*", "+@read", "+@write", "+ping"}, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if s.State != agentv1.State_STATE_RUNNING || s.Role != "master" || s.EngineVersion == "" || s.Maxmemory != 64<<20 {
		t.Fatalf("master status %+v", s)
	}
	if strings.Contains(s.EngineVersion, "valkey") || s.Config["maxmemory-policy"] != "allkeys-lru" || s.ConfigDigest == "" {
		t.Fatalf("status %+v", s)
	}
	s, err = m.Create(ctx, &agentv1.CreateInstance{Port: replica, Params: base0, RevisionId: "p00000001", Masterauth: "repl-secret",
		ReplicaOf: &agentv1.ReplicaOf{Host: "127.0.0.1", Port: master}})
	if err != nil {
		t.Fatal(err)
	}

	// 业务账号可读写；agent 管理账号不能读 value
	app, err := resp.Dial(ctx, l.Socket(master), "app", "app-pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Do(ctx, "SET", "k", "v"); err != nil {
		t.Fatal(err)
	}
	app.Close()
	st, _ := m.Load(master)
	adm, _ := m.Dial(ctx, st)
	if _, err := adm.Do(ctx, "GET", "k"); !resp.IsError(err, "NOPERM") {
		t.Fatalf("agent account GET: %v", err)
	}
	if _, err := adm.Do(ctx, "EVAL", "return redis.call('get','k')", "0"); !resp.IsError(err, "NOPERM") {
		t.Fatalf("agent account EVAL: %v", err)
	}
	adm.Close()
	// default 账号关闭
	if c, err := resp.Dial(ctx, l.Socket(master), "", ""); err == nil {
		if _, err := c.Do(ctx, "PING"); !resp.IsError(err, "NOAUTH") && !resp.IsError(err, "NOPERM") {
			t.Fatalf("default user usable: %v", err)
		}
		c.Close()
	}

	// 复制建立
	waitFor(t, func() bool { s := m.Status(ctx, replica); return s.MasterLinkStatus == "up" })
	if s := m.Status(ctx, master); s.ConnectedSlaves != 1 || len(s.Replicas) != 1 {
		t.Fatalf("replication: %+v", s)
	}

	// 1) 乐观锁冲突
	if _, err := m.Apply(ctx, &agentv1.ApplyParams{Port: master, RevisionId: "p00000003", ParentRevisionId: "p00000002", Params: map[string]string{"hz": "20"}}); err == nil || !strings.Contains(err.Error(), "CONFLICT") {
		t.Fatalf("conflict: %v", err)
	}
	// 2) 热改：CONFIG SET + REWRITE
	r, err := m.Apply(ctx, &agentv1.ApplyParams{Port: master, RevisionId: "p00000002", ParentRevisionId: "p00000001",
		Params: map[string]string{"maxmemory-policy": "allkeys-lfu", "hz": "20", "maxmemory": "128mb"}})
	if err != nil || r.Status != agentv1.CommandResult_OK {
		t.Fatalf("hot apply %+v %v", r, err)
	}
	conf, _ := os.ReadFile(l.ConfPath(master, agentv1.Kind_KIND_SERVER))
	if !strings.Contains(string(conf), "maxmemory-policy allkeys-lfu") || !strings.Contains(string(conf), "hz 20") {
		t.Fatalf("rewrite did not persist:\n%s", conf)
	}
	// 3) 需重启参数未声明 → 拒绝
	if _, err := m.Apply(ctx, &agentv1.ApplyParams{Port: master, RevisionId: "p00000003", ParentRevisionId: "p00000002", Params: map[string]string{"databases": "32"}}); err == nil {
		t.Fatal("undeclared restart parameter accepted")
	}
	// 4) 需重启参数 → PENDING_RESTART；之后再热改一次，pending 不能被 REWRITE 冲掉
	r, err = m.Apply(ctx, &agentv1.ApplyParams{Port: master, RevisionId: "p00000003", ParentRevisionId: "p00000002",
		Params: map[string]string{"databases": "32"}, RestartRequired: []string{"databases"}})
	if err != nil || r.Status != agentv1.CommandResult_PENDING_RESTART {
		t.Fatalf("restart apply %+v %v", r, err)
	}
	if _, err := m.Apply(ctx, &agentv1.ApplyParams{Port: master, RevisionId: "p00000004", ParentRevisionId: "p00000003", Params: map[string]string{"hz": "15"}}); err != nil {
		t.Fatal(err)
	}
	conf, _ = os.ReadFile(l.ConfPath(master, agentv1.Kind_KIND_SERVER))
	if !strings.Contains(string(conf), "databases 32") {
		t.Fatalf("pending parameter lost after rewrite:\n%s", conf)
	}
	if s := m.Status(ctx, master); s.State != agentv1.State_STATE_PENDING_RESTART {
		t.Fatalf("state %v", s.State)
	}
	// 6) 重启后 pending 生效并清空，数据还在（save "" 时无持久化：此处只验证配置）
	s, err = m.Action(ctx, master, agentv1.InstanceAction_RESTART)
	if err != nil {
		t.Fatal(err)
	}
	st, _ = m.Load(master)
	if len(st.Pending) != 0 || s.Config["databases"] != "32" || s.State != agentv1.State_STATE_RUNNING {
		t.Fatalf("after restart pending=%v databases=%q state=%v", st.Pending, s.Config["databases"], s.State)
	}
	// 7) 漂移：绕过控制台直接改
	adm, _ = m.Dial(ctx, st)
	_, _ = adm.Do(ctx, "CONFIG", "SET", "maxmemory-policy", "noeviction")
	adm.Close()
	if d := m.Drift(st, m.Status(ctx, master)); d["maxmemory-policy"] != "noeviction" {
		t.Fatalf("drift = %v", d)
	}
	// 8) 业务账号同步：删除 app，新增 app2
	if _, err := m.SyncACL(ctx, &agentv1.SyncAcl{Port: master, Users: []*agentv1.AclUser{{Name: "app2", PasswordSha256: []string{h}, Rules: []string{"~*", "+@read"}, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := resp.Dial(ctx, l.Socket(master), "app", "app-pass"); err == nil {
		t.Fatal("removed user can still log in")
	}
	if c, err := resp.Dial(ctx, l.Socket(master), "app2", "app-pass"); err != nil {
		t.Fatal(err)
	} else {
		c.Close()
	}
	acl, _ := os.ReadFile(l.ACLPath(master))
	if !strings.Contains(string(acl), "user app2 ") || strings.Contains(string(acl), "user app ") {
		t.Fatalf("ACL SAVE did not persist:\n%s", acl)
	}
	// 9) 协调切换：主从互换
	waitFor(t, func() bool { s := m.Status(ctx, replica); return s.MasterLinkStatus == "up" })
	if _, err := m.Failover(ctx, &agentv1.Failover{Port: master, Mode: agentv1.Failover_COORDINATED, TimeoutMs: 5000}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return m.Status(ctx, replica).Role == "master" && m.Status(ctx, master).Role == "slave" })
	// 10) 删除：运行中拒绝，停止后移入回收区
	if _, err := m.Action(ctx, replica, agentv1.InstanceAction_DELETE); err == nil {
		t.Fatal("deleted a running instance")
	}
	if _, err := m.Action(ctx, replica, agentv1.InstanceAction_STOP); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(ctx, replica); s.State != agentv1.State_STATE_STOPPED {
		t.Fatalf("stopped state %v %s", s.State, s.Error)
	}
	if _, err := m.Action(ctx, replica, agentv1.InstanceAction_DELETE); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.InstanceDir(replica)); !os.IsNotExist(err) {
		t.Fatal("instance dir still present")
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met in 10s")
}
