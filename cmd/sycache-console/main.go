// sycache-console：CacheOS Web 控制台。
//
// 一个进程同时提供：Web 控制台与 REST API（-listen），以及供节点主动连接的 gRPC 网关（-grpc-listen）。
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/alerts"
	"github.com/xiaoguan521/cacheos/internal/console/api"
	"github.com/xiaoguan521/cacheos/internal/console/backupstore"
	"github.com/xiaoguan521/cacheos/internal/console/gateway"
	"github.com/xiaoguan521/cacheos/internal/console/ha"
	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/orch"
	"github.com/xiaoguan521/cacheos/internal/console/pki"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
	"github.com/xiaoguan521/cacheos/internal/pkg/secretbox"
	"github.com/xiaoguan521/cacheos/internal/version"
	"github.com/xiaoguan521/cacheos/web"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "backup" || os.Args[1] == "restore") {
		os.Exit(backupMain(os.Args[1:], os.Stdout, os.Stderr))
	}
	listen := flag.String("listen", envOr("CACHEOS_LISTEN", "127.0.0.1:8443"), "Web 控制台监听地址（生产环境只在内网或前置 TLS）")
	tlsCert := flag.String("tls-cert", os.Getenv("CACHEOS_TLS_CERT"), "Web 控制台 TLS 证书（可选）")
	tlsKey := flag.String("tls-key", os.Getenv("CACHEOS_TLS_KEY"), "Web 控制台 TLS 私钥（可选）")
	grpcListen := flag.String("grpc-listen", envOr("CACHEOS_GRPC_LISTEN", "0.0.0.0:9443"), "agent 连接的 gRPC 监听地址")
	grpcHosts := flag.String("grpc-hosts", envOr("CACHEOS_GRPC_HOSTS", ""), "gRPC 服务端证书包含的主机名/IP，逗号分隔（agent 用它们连接）")
	grpcAdvertise := flag.String("grpc-advertise", envOr("CACHEOS_GRPC_ADVERTISE", ""), "写入主机安装命令的控制台地址 host:port")
	dataDir := flag.String("data", envOr("CACHEOS_DATA", "/var/lib/sycache-console"), "数据目录（数据库、CA、安装 ID）")
	dsn := flag.String("db", envOr("CACHEOS_DB", ""), "数据库：SQLite 文件路径或 postgres:// DSN（默认 <data>/console.db）")
	tlsAuto := flag.Bool("tls-auto", envBool("CACHEOS_TLS_AUTO"), "Web 控制台使用内置 Web CA 自动签发的 HTTPS 证书（未指定 -tls-cert 时）")
	webHosts := flag.String("web-hosts", envOr("CACHEOS_WEB_HOSTS", ""), "自动 HTTPS 证书包含的主机名/IP，逗号分隔（默认取 -grpc-hosts 并加上 localhost）")
	showVersion := flag.Bool("version", false, "打印版本并退出")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String("console"))
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	o := options{listen: *listen, tlsCert: *tlsCert, tlsKey: *tlsKey, tlsAuto: *tlsAuto, webHosts: *webHosts, grpcListen: *grpcListen,
		grpcHosts: *grpcHosts, grpcAdvertise: *grpcAdvertise, dataDir: *dataDir, dsn: *dsn}
	if err := run(log, o); err != nil {
		log.Error("sycache-console stopped", "err", err)
		os.Exit(1)
	}
}

type options struct {
	listen, tlsCert, tlsKey     string
	tlsAuto                     bool
	webHosts                    string
	grpcListen, grpcHosts       string
	grpcAdvertise, dataDir, dsn string
}

func run(log *slog.Logger, o options) error {
	listen, tlsCert, tlsKey, grpcListen, grpcHosts, grpcAdvertise, dataDir, dsn :=
		o.listen, o.tlsCert, o.tlsKey, o.grpcListen, o.grpcHosts, o.grpcAdvertise, o.dataDir, o.dsn
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if dsn == "" {
		dsn = filepath.Join(dataDir, "console.db")
	}
	st, err := store.Open(dsn)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	ca, err := pki.LoadOrCreate(filepath.Join(dataDir, "pki"))
	if err != nil {
		return fmt.Errorf("pki: %w", err)
	}
	cmdKey, err := commandKey(st)
	if err != nil {
		return err
	}
	lic := &licsvc.Service{Store: st, DataDir: dataDir, Keys: license.BuiltinKeyRing()}
	if err := lic.Init(); err != nil {
		return fmt.Errorf("license: %w", err)
	}
	lic.Tick(time.Now())
	aud := &audit.Logger{DB: st.DB}

	box, err := secretbox.LoadOrCreate(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return fmt.Errorf("secret key: %w", err)
	}
	// PostgreSQL 可被多台控制台共用（主备高可用，docs/ha.md）：所有控制台必须共用同一套 CA 与数据密钥，否则节点证书与库中加密数据在切换后不可用
	pg := store.IsPostgres(dsn)
	if pg {
		if err := sharedIdentity(st, "ha_secretbox_id", box.ID(), "secret.key"); err != nil {
			return err
		}
		if err := sharedIdentity(st, "ha_ca_fingerprint", pki.Fingerprint(ca.Cert.Raw), "pki/"); err != nil {
			return err
		}
	}
	backupDir := envOr("CACHEOS_BACKUP_DIR", filepath.Join(dataDir, "backups"))
	backupStorage, err := backupstore.NewFromEnv(dataDir)
	if err != nil {
		return fmt.Errorf("backup storage: %w", err)
	}
	gw := &gateway.Server{Store: st, CA: ca, CmdKey: cmdKey, Lic: lic, Audit: aud, Log: log, BackupDir: backupDir, BackupStore: backupStorage}
	orc := &orch.Service{Store: st, GW: gw, Box: box, Log: log, License: lic.Snapshot, CA: ca}
	if v, _ := st.GetKV("approval_required"); v == "1" {
		orc.RequireApproval = true
	}
	if err := orc.EnsureBuiltinTemplates(); err != nil {
		return fmt.Errorf("parameter templates: %w", err)
	}
	alr := &alerts.Service{Store: st, Box: box, Log: log, IMAllow: func() bool { return lic.Snapshot().Feature(license.FeatureAlertIM) }}
	el := &ha.Elector{Licensed: func() bool { return lic.Snapshot().Feature(license.FeatureHA) }, Log: log}
	if pg { // 共用 PostgreSQL 时总是选主，避免两台控制台同时下发
		if el.DB, err = st.DB.DB(); err != nil {
			return err
		}
	}
	gw.Ready = el.IsLeader

	hosts := splitList(grpcHosts)
	if len(hosts) == 0 {
		h, _ := os.Hostname()
		hosts = []string{h, "localhost", "127.0.0.1"}
	}
	if grpcAdvertise == "" {
		_, port, _ := net.SplitHostPort(grpcListen)
		grpcAdvertise = net.JoinHostPort(hosts[0], port)
	}
	tlsCfg, err := gw.TLSConfig(hosts)
	if err != nil {
		return err
	}
	gs := gw.GRPCServer(tlsCfg)
	gl, err := net.Listen("tcp", grpcListen)
	if err != nil {
		return err
	}

	// Web 控制台 HTTPS：-tls-cert 优先；否则 -tls-auto 用内置 Web CA（ECDSA，浏览器可用）自动签发并续期
	var webCA *pki.WebCA
	if tlsCert == "" && o.tlsAuto {
		if webCA, err = pki.LoadOrCreateWebCA(filepath.Join(dataDir, "pki")); err != nil {
			return fmt.Errorf("web CA: %w", err)
		}
	}
	useTLS := tlsCert != "" || webCA != nil
	if !useTLS && !loopback(listen) {
		log.Warn("Web 控制台未启用 HTTPS 却监听在非本机地址：登录凭据会以明文传输。生产环境请配置 -tls-cert/-tls-key 或 -tls-auto（CACHEOS_TLS_AUTO=1）",
			"listen", listen)
	}
	apiSrv := &api.Server{Store: st, GW: gw, Orch: orc, Alerts: alr, Lic: lic, Audit: aud, Box: box, Log: log, Web: web.Dist(),
		GRPCAddr: grpcAdvertise, CAFingerprint: pki.Fingerprint(ca.Cert.Raw), SecureCookie: useTLS, HA: el}
	if webCA != nil {
		apiSrv.WebCAPEM = webCA.CertPEM()
	}
	h, err := apiSrv.Handler()
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	if webCA != nil {
		wh := splitList(o.webHosts)
		if len(wh) == 0 {
			wh = append(append([]string{}, hosts...), "localhost", "127.0.0.1")
		}
		hs.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: webCA.GetCertificate(wh)}
		log.Info("web console HTTPS uses the built-in web CA; import it into browsers once", "hosts", strings.Join(wh, ","),
			"ca_download", "/web-ca.crt", "ca_file", filepath.Join(dataDir, "pki", "web-ca.crt"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// 后台任务只在主控制台运行；单机模式下本进程立即成为主
	lead := func(lctx context.Context) {
		// 接管时所有节点先视为离线，等待重新连接
		hq := query.Use(st.DB).Host
		if _, err := hq.WithContext(lctx).Where(hq.Status.Eq("online")).Update(hq.Status, "offline"); err != nil {
			log.Warn("reset host status", "err", err)
		}
		lic.Tick(time.Now())
		apiSrv.RecoverMigrations()
		go housekeeping(lctx, st, lic, gw, log)
		go alr.Run(lctx)
	}
	go el.Run(ctx, lead, gw.DisconnectAll)

	errc := make(chan error, 2)
	go func() { errc <- gs.Serve(gl) }()
	go func() {
		if tlsCert != "" {
			errc <- hs.ListenAndServeTLS(tlsCert, tlsKey)
		} else if webCA != nil {
			errc <- hs.ListenAndServeTLS("", "")
		} else {
			errc <- hs.ListenAndServe()
		}
	}()
	log.Info("sycache-console started", "version", version.Version, "web", listen, "grpc", grpcListen,
		"grpc_advertise", grpcAdvertise, "grpc_hosts", strings.Join(hosts, ","), "installation_id", lic.InstallationID(), "ha", el.Enabled())

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	gs.GracefulStop()
	return nil
}

// sharedIdentity 校验高可用集群中各控制台的共享密钥材料一致：首台写入指纹，之后启动的控制台必须相同。
func sharedIdentity(st *store.Store, key, val, file string) error {
	got, err := st.InitKV(key, val)
	if err != nil {
		return err
	}
	if got != val {
		return fmt.Errorf("ha: %s differs from the other consoles; copy the data directory's %s from the first console", key, file)
	}
	return nil
}

// commandKey 读取或生成指令签名密钥（Ed25519），私钥只保存在控制台数据库。
func commandKey(st *store.Store) (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	// 只在不存在时写入：高可用下并发启动的控制台得到同一把密钥
	v, err := st.InitKV("command_key", base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("corrupt command_key")
	}
	return ed25519.PrivateKey(b), nil
}

// housekeeping：刷新授权、执行每日 RDB 备份策略并清理分钟指标。
func housekeeping(ctx context.Context, st *store.Store, lic *licsvc.Service, gw *gateway.Server, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			lic.Tick(now)
			runScheduledBackups(ctx, st, lic, gw, log, now)
			m := query.Use(st.DB).MetricMinute
			if _, err := m.WithContext(ctx).Where(m.Minute.Lt(now.Add(-7 * 24 * time.Hour).Unix())).Delete(); err != nil {
				slog.Warn("prune metrics", "err", err)
			}
		}
	}
}

func runScheduledBackups(ctx context.Context, st *store.Store, lic *licsvc.Service, gw *gateway.Server, log *slog.Logger, now time.Time) {
	pq := query.Use(st.DB).BackupPolicy
	policies, err := pq.WithContext(ctx).Where(pq.Enabled).Find()
	if err != nil {
		log.Warn("load backup policies", "err", err)
		return
	}
	for _, policy := range policies {
		if policy.Cron != now.Format("15:04") || policy.LastRunAt != nil && policy.LastRunAt.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02") {
			continue
		}
		// Mark before dispatch so a slow backup cannot be started twice by two
		// minute ticks. A failed run is still visible in the audit/backup record.
		last := now
		policy.LastRunAt = &last
		if err := pq.WithContext(ctx).Save(policy); err != nil {
			log.Warn("mark backup policy", "unit", policy.UnitID, "err", err)
			continue
		}
		iq := query.Use(st.DB).Instance
		instances, err := iq.WithContext(ctx).Where(iq.UnitID.Eq(policy.UnitID), iq.Kind.Eq("server")).Order(iq.ID).Find()
		if err != nil || len(instances) == 0 {
			log.Warn("scheduled backup has no server", "unit", policy.UnitID, "err", err)
			continue
		}
		in := instances[0]
		for _, candidate := range instances {
			if candidate.Role == "master" || candidate.Desired == "master" {
				in = candidate
				break
			}
		}
		upload := policy.Upload && lic.Snapshot().Feature(license.FeatureRemoteBkp)
		if policy.Upload && !upload {
			log.Warn("scheduled remote backup skipped: license feature unavailable", "unit", policy.UnitID)
			continue
		}
		backupID := fmt.Sprintf("s-%d", now.UnixNano())
		res, sendErr := gw.Send(ctx, in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Backup{Backup: &agentv1.Backup{Port: in.Port, BackupId: backupID, Upload: upload}}}, 3*time.Minute)
		if sendErr != nil || res.Status != agentv1.CommandResult_OK {
			msg := "unknown backup failure"
			if sendErr != nil {
				msg = sendErr.Error()
			} else {
				msg = res.Message
			}
			_ = query.Use(st.DB).Backup.WithContext(ctx).Create(&store.Backup{ID: backupID, UnitID: policy.UnitID, InstanceID: in.ID, Location: map[bool]string{true: "console", false: "local"}[upload], Status: "failed", Trigger: "schedule", Error: msg, CreatedBy: "scheduler", CreatedAt: now})
			log.Warn("scheduled backup failed", "unit", policy.UnitID, "err", msg)
			continue
		}
		if !upload {
			var meta struct {
				Size   int64  `json:"size"`
				SHA256 string `json:"sha256"`
			}
			_ = json.Unmarshal(res.Payload, &meta)
			_ = query.Use(st.DB).Backup.WithContext(ctx).Create(&store.Backup{ID: backupID, UnitID: policy.UnitID, InstanceID: in.ID, Location: "local", Size: meta.Size, SHA256: meta.SHA256, Status: "done", Trigger: "schedule", CreatedBy: "scheduler", CreatedAt: now})
		} else if row, err := query.Use(st.DB).Backup.WithContext(ctx).Where(query.Use(st.DB).Backup.ID.Eq(backupID)).First(); err == nil {
			row.Trigger, row.CreatedBy = "schedule", "scheduler"
			_ = query.Use(st.DB).Backup.WithContext(ctx).Save(row)
		}
		if policy.KeepDays > 0 {
			cutoff := now.Add(-time.Duration(policy.KeepDays) * 24 * time.Hour)
			bq := query.Use(st.DB).Backup
			expired, findErr := bq.WithContext(ctx).Where(bq.UnitID.Eq(policy.UnitID), bq.Status.Eq("done"), bq.CreatedAt.Lt(cutoff)).Find()
			if findErr != nil {
				log.Warn("find expired backups", "unit", policy.UnitID, "err", findErr)
				continue
			}
			for _, row := range expired {
				if err := gw.RemoveBackup(ctx, row); err != nil {
					log.Warn("remove expired backup", "unit", policy.UnitID, "backup", row.ID, "err", err)
					continue
				}
				expiredAt := now
				row.Status, row.ExpiresAt, row.Error = "expired", &expiredAt, ""
				if err := bq.WithContext(ctx).Save(row); err != nil {
					log.Warn("mark expired backup", "unit", policy.UnitID, "backup", row.ID, "err", err)
				}
			}
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envBool 接受 1/true/TRUE 等写法（早期版本的 console.env 写的是 true）。
func envBool(k string) bool {
	b, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(k)))
	return b
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loopback 判断监听地址是否只在本机。
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
