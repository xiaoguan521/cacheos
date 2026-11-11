package instance

// 用真实的 sycache-server 验证实例生命周期与参数状态机。
// 运行：SYCACHE_BIN=<.../home/atwasoft/sycache/bin> go test ./internal/agent/instance/ -run Integration -v
// 未设置 SYCACHE_BIN 时跳过（CI 的 make agent-it 会设置）。

import (
	"context"
	"encoding/json"
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
		binary := "sycache-server"
		if _, err := os.Stat(filepath.Join(l.Root, "instances", port, "sycache-sentinel.conf")); err == nil {
			conf = filepath.Join(l.Root, "instances", port, "sycache-sentinel.conf")
			binary = "sycache-sentinel"
		}
		out, err := exec.Command(filepath.Join(bin, binary), conf, "--daemonize", "yes").CombinedOutput()
		if err != nil {
			t.Logf("start %s failed: %v output=%s", port, err, out)
		} else if len(out) > 0 {
			t.Logf("start %s output=%s", port, out)
		}
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
	if strings.Contains(s.EngineVersion, "val"+"key") || s.Config["maxmemory-policy"] != "allkeys-lru" || s.ConfigDigest == "" {
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

func TestIntegrationNativeMigrationCutover(t *testing.T) {
	bin := os.Getenv("SYCACHE_BIN")
	if bin == "" {
		t.Skip("SYCACHE_BIN not set")
	}
	base, err := os.MkdirTemp("/tmp", "sc-migrate")
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Root: base + "/opt", RunDir: base + "/run", LogDir: base + "/log", StateDir: base + "/state"}
	for _, d := range []string{l.Root + "/instances", l.RunDir, l.LogDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	m := &Manager{L: l}
	m.Run = fakeWrapper(t, bin, l)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sourcePort := freePort(t)
	targetPort := freePort(t)
	for targetPort == sourcePort {
		targetPort = freePort(t)
	}
	t.Cleanup(func() {
		for _, p := range []uint32{sourcePort, targetPort} {
			_, _ = m.Run(context.Background(), "stop", strconv.Itoa(int(p)))
		}
		os.RemoveAll(base)
	})
	appHash := sha("app-pass")
	params := map[string]string{"maxmemory": "64mb", "maxmemory-policy": "allkeys-lru", "save": "", "appendonly": "no"}
	create := func(port uint32) {
		t.Helper()
		if _, err := m.Create(ctx, &agentv1.CreateInstance{
			Port:       port,
			Params:     params,
			RevisionId: "p00000001",
			Masterauth: "repl-secret",
			AclUsers:   []*agentv1.AclUser{{Name: "app", PasswordSha256: []string{appHash}, Rules: []string{"~*", "+@read", "+@write", "+ping"}, Enabled: true}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	create(sourcePort)
	create(targetPort)

	source, err := resp.Dial(ctx, l.Socket(sourcePort), "app", "app-pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Do(ctx, "SET", "migration:first", "one"); err != nil {
		source.Close()
		t.Fatal(err)
	}

	address := fmt.Sprintf("127.0.0.1:%d", sourcePort)
	initial, err := m.Migrate(ctx, &agentv1.Migrate{
		Port:           targetPort,
		JobId:          "integration-native-migration",
		Mode:           agentv1.Migrate_FULL_INCREMENTAL,
		Pattern:        "*",
		SourceAddress:  address,
		SourceUser:     UserRepl,
		SourcePassword: "repl-secret",
		SourceQuiesced: false,
	})
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	var initialReport migrateReport
	if err := json.Unmarshal(initial, &initialReport); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if !initialReport.AwaitingCutover || !initialReport.FullSyncDone || initialReport.Strategy != "replication" {
		source.Close()
		t.Fatalf("initial migration report %+v", initialReport)
	}

	if _, err := source.Do(ctx, "SET", "migration:second", "two"); err != nil {
		source.Close()
		t.Fatal(err)
	}
	target, err := resp.Dial(ctx, l.Socket(targetPort), "app", "app-pass")
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		v, err := target.Do(ctx, "GET", "migration:second")
		return err == nil && v.Text() == "two"
	})

	final, err := m.Migrate(ctx, &agentv1.Migrate{
		Port:           targetPort,
		JobId:          "integration-native-migration",
		Mode:           agentv1.Migrate_FULL_INCREMENTAL,
		Pattern:        "*",
		SourceAddress:  address,
		SourceUser:     UserRepl,
		SourcePassword: "repl-secret",
		SourceQuiesced: true,
		Finalize:       true,
	})
	source.Close()
	target.Close()
	if err != nil {
		t.Fatal(err)
	}
	var finalReport migrateReport
	if err := json.Unmarshal(final, &finalReport); err != nil {
		t.Fatal(err)
	}
	if !finalReport.CutoverReady || finalReport.Strategy != "replication" {
		t.Fatalf("final migration report %+v", finalReport)
	}
	waitFor(t, func() bool { return m.Status(ctx, targetPort).Role == "master" })
	reader, err := resp.Dial(ctx, l.Socket(targetPort), "app", "app-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	v, err := reader.Do(ctx, "GET", "migration:first")
	if err != nil || v.Text() != "one" {
		t.Fatalf("promoted target lost data: value=%q err=%v", v.Text(), err)
	}
	v, err = reader.Do(ctx, "GET", "migration:second")
	if err != nil || v.Text() != "two" {
		t.Fatalf("promoted target lost streamed data: value=%q err=%v", v.Text(), err)
	}
	conf, err := os.ReadFile(l.ConfPath(targetPort, agentv1.Kind_KIND_SERVER))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "masterauth repl-secret") || strings.Contains(string(conf), "masteruser sycache-repl") {
		t.Fatalf("temporary replication credentials remained in target config:\n%s", conf)
	}
}

func TestIntegrationSentinelAuthAgainstDataNode(t *testing.T) {
	bin := os.Getenv("SYCACHE_BIN")
	if bin == "" {
		t.Skip("SYCACHE_BIN not set")
	}
	base, err := os.MkdirTemp("/tmp", "sc-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Root: base + "/opt", RunDir: base + "/run", LogDir: base + "/log", StateDir: base + "/state"}
	for _, d := range []string{l.Root + "/instances", l.RunDir, l.LogDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	m := &Manager{L: l}
	m.Run = fakeWrapper(t, bin, l)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	masterPort, sentinelPort := freePort(t), freePort(t)
	for sentinelPort == masterPort {
		sentinelPort = freePort(t)
	}
	t.Cleanup(func() {
		for _, p := range []uint32{masterPort, sentinelPort} {
			_, _ = m.Run(context.Background(), "stop", strconv.Itoa(int(p)))
		}
		os.RemoveAll(base)
	})
	params := map[string]string{"maxmemory": "64mb", "save": "", "appendonly": "no"}
	if _, err := m.Create(ctx, &agentv1.CreateInstance{Port: masterPort, Params: params, RevisionId: "p00000001", Masterauth: "repl-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, &agentv1.CreateInstance{
		Port:                sentinelPort,
		Kind:                agentv1.Kind_KIND_SENTINEL,
		RevisionId:          "p00000001",
		Masterauth:          "repl-secret",
		SentinelMonitorName: "mymaster",
		SentinelMasterHost:  "127.0.0.1",
		SentinelMasterPort:  masterPort,
		SentinelQuorum:      1,
	}); err != nil {
		t.Fatal(err)
	}
	c, err := m.Dial(ctx, &State{Port: sentinelPort, Kind: agentv1.Kind_KIND_SENTINEL, AdminPassword: func() string {
		st, loadErr := m.Load(sentinelPort)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return st.AdminPassword
	}()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, func() bool {
		v, err := c.Do(ctx, "SENTINEL", "GET-MASTER-ADDR-BY-NAME", "mymaster")
		return err == nil && len(v.Array) == 2 && v.Array[0].Text() == "127.0.0.1" && uint32(atoi(v.Array[1].Text())) == masterPort
	})
	if role := m.Status(ctx, sentinelPort).Role; role != "sentinel" {
		t.Fatalf("sentinel role=%q", role)
	}
}

func TestIntegrationClusterMigrateAuthentication(t *testing.T) {
	bin := os.Getenv("SYCACHE_BIN")
	if bin == "" {
		t.Skip("SYCACHE_BIN not set")
	}
	base, err := os.MkdirTemp("/tmp", "sc-cluster")
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Root: base + "/opt", RunDir: base + "/run", LogDir: base + "/log", StateDir: base + "/state"}
	for _, d := range []string{l.Root + "/instances", l.RunDir, l.LogDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	m := &Manager{L: l}
	m.Run = fakeWrapper(t, bin, l)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sourcePort, targetPort := freePort(t), freePort(t)
	for sourcePort > 55535 {
		sourcePort = freePort(t)
	}
	for targetPort == sourcePort || targetPort > 55535 {
		targetPort = freePort(t)
	}
	t.Cleanup(func() {
		for _, p := range []uint32{sourcePort, targetPort} {
			_, _ = m.Run(context.Background(), "stop", strconv.Itoa(int(p)))
		}
		os.RemoveAll(base)
	})
	appHash := sha("app-pass")
	params := map[string]string{"maxmemory": "64mb", "save": "", "appendonly": "no"}
	for _, port := range []uint32{sourcePort, targetPort} {
		if _, err := m.Create(ctx, &agentv1.CreateInstance{
			Port:           port,
			Params:         params,
			RevisionId:     "p00000001",
			Masterauth:     "repl-secret",
			ClusterEnabled: true,
			AclUsers:       []*agentv1.AclUser{{Name: "app", PasswordSha256: []string{appHash}, Rules: []string{"~*", "+@read", "+@write", "+ping"}, Enabled: true}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	nodeID := func(raw []byte, port uint32) string {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.HasPrefix(fields[1], fmt.Sprintf("127.0.0.1:%d@", port)) {
				return fields[0]
			}
		}
		return ""
	}
	targetNodes, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: targetPort, Op: agentv1.ClusterOp_NODES})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := ""
	targetID := nodeID(targetNodes, targetPort)
	if targetID == "" {
		t.Fatalf("target node ID missing: %s", targetNodes)
	}
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: sourcePort, Op: agentv1.ClusterOp_MEET, PeerHost: "127.0.0.1", PeerPort: targetPort}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		raw, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: sourcePort, Op: agentv1.ClusterOp_NODES})
		if err != nil {
			return false
		}
		sourceID = nodeID(raw, sourcePort)
		return sourceID != "" && strings.Contains(string(raw), targetID)
	})
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: sourcePort, Op: agentv1.ClusterOp_ADD_SLOTS, Slots: []*agentv1.SlotRange{{Start: 0, End: 8191}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: targetPort, Op: agentv1.ClusterOp_ADD_SLOTS, Slots: []*agentv1.SlotRange{{Start: 8192, End: 16383}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, port := range []uint32{sourcePort, targetPort} {
			st, err := m.Load(port)
			if err != nil {
				return false
			}
			c, err := m.Dial(ctx, st)
			if err != nil {
				return false
			}
			v, err := c.Do(ctx, "CLUSTER", "INFO")
			c.Close()
			if err != nil || resp.ParseInfo(v.Text())["cluster_state"] != "ok" {
				return false
			}
		}
		return true
	})
	st, err := m.Load(sourcePort)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := m.Dial(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	key := ""
	for i := 0; i < 100000; i++ {
		candidate := fmt.Sprintf("migrate:%d", i)
		v, err := admin.Do(ctx, "CLUSTER", "KEYSLOT", candidate)
		if err == nil && v.Int == 0 {
			key = candidate
			break
		}
	}
	admin.Close()
	if key == "" {
		t.Fatal("could not find a test key in slot 0")
	}
	source, err := resp.Dial(ctx, l.Socket(sourcePort), "app", "app-pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Do(ctx, "SET", key, "moved"); err != nil {
		source.Close()
		t.Fatal(err)
	}
	source.Close()
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: targetPort, Op: agentv1.ClusterOp_SETSLOT_IMPORTING, Slot: 0, NodeId: sourceID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: sourcePort, Op: agentv1.ClusterOp_SETSLOT_MIGRATING, Slot: 0, NodeId: targetID}); err != nil {
		t.Fatal(err)
	}
	payload, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{
		Port:         sourcePort,
		Op:           agentv1.ClusterOp_MIGRATE_SLOT,
		PeerHost:     "127.0.0.1",
		PeerPort:     targetPort,
		PeerUser:     UserCluster,
		PeerPassword: "repl-secret",
		Slot:         0,
		Batch:        100,
		TimeoutMs:    10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "\"migrated\":1") {
		t.Fatalf("unexpected migrate result: %s", payload)
	}
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: targetPort, Op: agentv1.ClusterOp_SETSLOT_NODE, Slot: 0, NodeId: targetID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ClusterOp(ctx, &agentv1.ClusterOp{Port: sourcePort, Op: agentv1.ClusterOp_SETSLOT_NODE, Slot: 0, NodeId: targetID}); err != nil {
		t.Fatal(err)
	}
	target, err := resp.Dial(ctx, l.Socket(targetPort), "app", "app-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	v, err := target.Do(ctx, "GET", key)
	if err != nil || v.Text() != "moved" {
		t.Fatalf("target did not receive migrated key: value=%q err=%v", v.Text(), err)
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
