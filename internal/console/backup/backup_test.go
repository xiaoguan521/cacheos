package backup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

// 本包测试关注备份内容与一致性，用轻量的口令派生参数加快运行；生产参数由 TestProductionKDF 覆盖。
func TestMain(m *testing.M) {
	kdfTime, kdfMemoryKiB, kdfThreads = 1, 8*1024, 1
	os.Exit(m.Run())
}

// 生产参数下完整备份、恢复一次；并确认用轻量参数无法解开生产参数的备份（参数确实生效）。
func TestProductionKDF(t *testing.T) {
	saved := [3]uint32{kdfTime, kdfMemoryKiB, kdfThreads}
	kdfTime, kdfMemoryKiB, kdfThreads = 3, 64*1024, 4
	defer func() { kdfTime, kdfMemoryKiB, kdfThreads = saved[0], saved[1], saved[2] }()
	dir := t.TempDir()
	src := storetest.Open(t)
	seed(t, src, dir)
	pass := []byte("production parameters")
	var buf bytes.Buffer
	if _, err := Create(&buf, src, dir, pass, Options{}); err != nil {
		t.Fatal(err)
	}
	kdfTime, kdfMemoryKiB, kdfThreads = 1, 8*1024, 1
	if _, err := Restore(bytes.NewReader(buf.Bytes()), storetest.Open(t), t.TempDir(), pass, false); err == nil {
		t.Fatal("backup made with production parameters opened with different parameters")
	}
	kdfTime, kdfMemoryKiB, kdfThreads = 3, 64*1024, 4
	if _, err := Restore(bytes.NewReader(buf.Bytes()), storetest.Open(t), t.TempDir(), pass, false); err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T, st *store.Store, dir string) {
	t.Helper()
	db := st.DB
	db.Create(&store.User{Username: "admin", PasswordHash: "h", Role: store.RoleAdmin})
	db.Create(&store.Host{Name: "cache-1", Status: "online", Zone: "rack-a", EngineCurrent: "1.0.0"})
	u := &store.Unit{Name: "orders", Mode: store.ModeReplication, RevisionID: "p00000002", MasterAuth: "v1:sealed-auth", Status: "running"}
	db.Create(u)
	db.Create(&store.Instance{UnitID: u.ID, HostID: 1, Port: 7001, Kind: "server", Desired: "master", Drifted: true})
	db.Create(&store.Instance{UnitID: u.ID, HostID: 1, Port: 7002, Kind: "server", Desired: "replica"})
	db.Create(&store.ParamRevision{ID: "p00000001", UnitID: u.ID, Params: `{"maxmemory":"1gb"}`, Digest: "sha256:a"})
	db.Create(&store.ParamRevision{ID: "p00000002", UnitID: u.ID, ParentID: "p00000001", Params: `{"maxmemory":"2gb"}`, Digest: "sha256:b"})
	db.Create(&store.AclAccount{UnitID: u.ID, Name: "app", PassHashes: "abc,def", KeyPatterns: "app:*", Categories: "+@read", Enabled: true})
	db.Create(&store.EnginePackage{Name: "sycache", Version: "1.0.0", Arch: "x86_64", Data: []byte{0x1f, 0x8b, 0, 1, 2, 255}})
	db.Create(&store.MetricMinute{InstanceID: 1, Minute: 1, OpsPerSec: 9})
	_ = st.SetKV("command_key", "secret-ed25519")
	al := &audit.Logger{DB: db}
	for i := 0; i < 3; i++ {
		_ = al.Write(audit.Entry{Username: "admin", Action: "unit.params", ObjectType: "unit", ObjectID: "orders"})
	}
	_ = os.MkdirAll(filepath.Join(dir, "pki"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "pki", "ca.key"), []byte("CA-KEY"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pki", "ca.crt"), []byte("CA-CRT"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "secret.key"), bytes.Repeat([]byte{9}, 32), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "installation.json"), []byte(`{"installation_id":"inst_abc"}`), 0o600)
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := storetest.Open(t)
	seed(t, src, srcDir)
	pass := []byte("correct horse battery staple")

	var buf bytes.Buffer
	sum, err := Create(&buf, src, srcDir, pass, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("CA-KEY")) || bytes.Contains(buf.Bytes(), []byte("secret-ed25519")) {
		t.Fatal("backup is not encrypted")
	}
	if sum.Tables["param_revisions"] != 2 || sum.Tables["instances"] != 2 || sum.Tables["audit_logs"] != 3 || sum.Tables["metric_minutes"] != 0 {
		t.Fatalf("summary: %+v", sum.Tables)
	}

	if _, err := Restore(bytes.NewReader(buf.Bytes()), storetest.Open(t), t.TempDir(), []byte("wrong"), false); err == nil || !strings.Contains(err.Error(), "口令") {
		t.Fatalf("wrong passphrase accepted: %v", err)
	}
	bad := append([]byte(nil), buf.Bytes()...)
	bad[len(bad)-5] ^= 1
	if _, err := Restore(bytes.NewReader(bad), storetest.Open(t), t.TempDir(), pass, false); err == nil {
		t.Fatal("tampered backup accepted")
	}

	dst := storetest.Open(t)
	if _, err := Restore(bytes.NewReader(buf.Bytes()), dst, dstDir, pass, false); err != nil {
		t.Fatal(err)
	}
	var revs []store.ParamRevision
	dst.DB.Order("seq").Find(&revs)
	if len(revs) != 2 || revs[0].ID != "p00000001" || revs[1].Params != `{"maxmemory":"2gb"}` {
		t.Fatalf("revisions not restored: %+v", revs)
	}
	var unit store.Unit
	dst.DB.First(&unit)
	if unit.MasterAuth != "v1:sealed-auth" || unit.RevisionID != "p00000002" {
		t.Fatalf("unit fields lost: %+v", unit)
	}
	var inst store.Instance
	dst.DB.First(&inst)
	if !inst.Drifted || inst.Port != 7001 {
		t.Fatalf("instance fields lost: %+v", inst)
	}
	var acc store.AclAccount
	dst.DB.First(&acc)
	if acc.PassHashes != "abc,def" || !acc.Enabled {
		t.Fatalf("acl account lost: %+v", acc)
	}
	var pkg store.EnginePackage
	dst.DB.First(&pkg)
	if !bytes.Equal(pkg.Data, []byte{0x1f, 0x8b, 0, 1, 2, 255}) {
		t.Fatalf("package not restored byte for byte: %v", pkg.Data)
	}
	if v, _ := dst.GetKV("command_key"); v != "secret-ed25519" {
		t.Fatal("system kv not restored")
	}
	if bad, err := (&audit.Logger{DB: dst.DB}).Verify(); err != nil || bad != 0 {
		t.Fatalf("audit chain broken after restore at %d: %v", bad, err)
	}
	for _, f := range []string{"pki/ca.key", "secret.key", "installation.json"} {
		a, _ := os.ReadFile(filepath.Join(srcDir, f))
		b, err := os.ReadFile(filepath.Join(dstDir, f))
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("%s not restored: %v", f, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dstDir, "pki", "ca.key")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("ca.key mode %v", fi.Mode().Perm())
	}
	// 新写入的记录不与恢复的主键冲突（PostgreSQL 序列已重置）
	if err := dst.DB.Create(&store.User{Username: "ops", PasswordHash: "h", Role: store.RoleViewer}).Error; err != nil {
		t.Fatalf("insert after restore: %v", err)
	}
	if err := (&audit.Logger{DB: dst.DB}).Write(audit.Entry{Username: "ops", Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if bad, err := (&audit.Logger{DB: dst.DB}).Verify(); err != nil || bad != 0 {
		t.Fatal("audit chain broken after a new entry")
	}

	// 目标库非空时拒绝，避免覆盖正在使用的控制台
	if _, err := Restore(bytes.NewReader(buf.Bytes()), dst, t.TempDir(), pass, false); err == nil || !strings.Contains(err.Error(), "不是空库") {
		t.Fatalf("restore into a non-empty database accepted: %v", err)
	}
}

func TestBackupIncludesMetricsWhenAsked(t *testing.T) {
	dir := t.TempDir()
	src := storetest.Open(t)
	seed(t, src, dir)
	var buf bytes.Buffer
	sum, err := Create(&buf, src, dir, []byte("pass phrase 123"), Options{Metrics: true})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tables["metric_minutes"] != 1 {
		t.Fatalf("metrics not included: %+v", sum.Tables)
	}
}

// SQLite → PostgreSQL：备份恢复也是迁移数据库的方式（需要 CACHEOS_TEST_DB 指向 PostgreSQL）。
func TestMigrateSQLiteToPostgres(t *testing.T) {
	if os.Getenv("CACHEOS_TEST_DB") == "" {
		t.Skip("set CACHEOS_TEST_DB to a PostgreSQL DSN")
	}
	srcDir := t.TempDir()
	src, err := store.Open(filepath.Join(srcDir, "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	seed(t, src, srcDir)
	var buf bytes.Buffer
	pass := []byte("migrate to postgres")
	if _, err := Create(&buf, src, srcDir, pass, Options{Metrics: true}); err != nil {
		t.Fatal(err)
	}
	dst := storetest.Open(t) // PostgreSQL
	if dst.DB.Dialector.Name() != "postgres" {
		t.Fatalf("destination is %s", dst.DB.Dialector.Name())
	}
	if _, err := Restore(bytes.NewReader(buf.Bytes()), dst, t.TempDir(), pass, false); err != nil {
		t.Fatal(err)
	}
	var revs int64
	dst.DB.Model(&store.ParamRevision{}).Count(&revs)
	if revs != 2 {
		t.Fatalf("revisions: %d", revs)
	}
	if bad, err := (&audit.Logger{DB: dst.DB}).Verify(); err != nil || bad != 0 {
		t.Fatalf("audit chain broken after migration: %d %v", bad, err)
	}
	if err := dst.DB.Create(&store.ParamRevision{ID: "p00000003", UnitID: 1}).Error; err != nil {
		t.Fatalf("new revision after migration: %v", err)
	}
}

// 备份在一个事务里读取所有表：并发写入不会让备份出现半个变更（参数版本有了、变更记录还没有）。
func TestBackupIsConsistentSnapshot(t *testing.T) {
	dir := t.TempDir()
	st := storetest.Open(t)
	seed(t, st, dir)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 3; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = st.DB.Transaction(func(tx *gorm.DB) error {
				id := fmt.Sprintf("p%08d", i)
				if err := tx.Create(&store.ParamRevision{ID: id, UnitID: 1}).Error; err != nil {
					return err
				}
				return tx.Create(&store.Operation{UnitID: 1, Kind: "params", Status: store.OpApplied, Note: id}).Error
			})
		}
	}()
	pass := []byte("snapshot pass")
	for k := 0; k < 5; k++ {
		var buf bytes.Buffer
		if _, err := Create(&buf, st, dir, pass, Options{}); err != nil {
			t.Fatal(err)
		}
		dst := storetest.Open(t)
		if _, err := Restore(bytes.NewReader(buf.Bytes()), dst, t.TempDir(), pass, false); err != nil {
			t.Fatal(err)
		}
		var revs, deps int64
		dst.DB.Model(&store.ParamRevision{}).Count(&revs)
		dst.DB.Model(&store.Operation{}).Count(&deps)
		// 种子数据：2 个参数版本、0 次变更；之后每个版本恰好对应一次变更
		if revs-2 != deps {
			close(stop)
			<-done
			t.Fatalf("inconsistent snapshot: %d revisions vs %d operations", revs, deps)
		}
	}
	close(stop)
	<-done
}

// 控制台启动过但还没初始化（只有启动时生成的 system_kvs）时允许恢复，以备份中的密钥为准。
func TestRestoreIntoStartedButEmptyConsole(t *testing.T) {
	dir := t.TempDir()
	src := storetest.Open(t)
	seed(t, src, dir)
	var buf bytes.Buffer
	pass := []byte("pass phrase 123")
	if _, err := Create(&buf, src, dir, pass, Options{}); err != nil {
		t.Fatal(err)
	}
	dst := storetest.Open(t)
	_ = dst.SetKV("command_key", "generated-at-startup")
	if _, err := Restore(bytes.NewReader(buf.Bytes()), dst, t.TempDir(), pass, false); err != nil {
		t.Fatal(err)
	}
	if v, _ := dst.GetKV("command_key"); v != "secret-ed25519" {
		t.Fatalf("command_key = %q", v)
	}
}
