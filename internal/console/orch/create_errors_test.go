package orch

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/secretbox"
)

// recordingSender 记录发往 agent 的指令；创建请求都返回成功。
type recordingSender struct {
	mu    sync.Mutex
	sends []*agentv1.ServerMessage
}

func (r *recordingSender) Online(uint) bool { return true }
func (r *recordingSender) Send(_ context.Context, _ uint, m *agentv1.ServerMessage, _ time.Duration) (*agentv1.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, m)
	return &agentv1.CommandResult{Status: agentv1.CommandResult_OK}, nil
}

// createFixture 建一个主从形态的部署单元、一台主机，返回编排服务与单元。
func createFixture(t *testing.T) (*Service, *recordingSender, *store.Unit) {
	t.Helper()
	st := storetest.Open(t)
	box, err := secretbox.LoadOrCreate(filepath.Join(t.TempDir(), "box.key"))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := box.Seal("pw")
	if err != nil {
		t.Fatal(err)
	}
	h := store.Host{Name: "h1", Hostname: "h1.local", Address: "10.0.0.1"}
	if err := st.DB.Create(&h).Error; err != nil {
		t.Fatal(err)
	}
	u := store.Unit{Name: "u1", Mode: store.ModeReplication, MasterAuth: auth, RevisionID: "p00000001"}
	if err := st.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	snd := &recordingSender{}
	svc := &Service{Store: st, GW: snd, Box: box, Log: slog.New(slog.DiscardHandler)}
	return svc, snd, &u
}

// 副本的主库不在本次创建的实例里时，必须报错，而不是把它静默创建成独立主库。
func TestRunCreateRefusesReplicaWhoseMasterIsNotReady(t *testing.T) {
	svc, snd, u := createFixture(t)
	replica := &store.Instance{UnitID: u.ID, HostID: 1, Port: 7001, Kind: "server", Desired: "replica"}
	if err := svc.Store.DB.Create(replica).Error; err != nil {
		t.Fatal(err)
	}
	r := &opRun{s: svc, op: &store.Operation{ID: 1, UnitID: u.ID}}
	if err := svc.runCreate(context.Background(), r, u, map[string]string{}, []*store.Instance{replica}); err == nil {
		t.Fatal("runCreate succeeded; want an error for a replica without a ready master")
	}
	if len(snd.sends) != 0 {
		t.Fatalf("%d commands sent to agents; the replica must not be created as a standalone master", len(snd.sends))
	}
}

// 读取业务账号失败时必须停止创建；否则会得到没有任何业务账号的实例。
func TestRunCreateStopsWhenACLUsersCannotBeLoaded(t *testing.T) {
	svc, snd, u := createFixture(t)
	master := &store.Instance{UnitID: u.ID, HostID: 1, Port: 7001, Kind: "server", Desired: "master"}
	if err := svc.Store.DB.Create(master).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.DB.Migrator().DropTable(&store.AclAccount{}); err != nil {
		t.Fatal(err)
	}
	r := &opRun{s: svc, op: &store.Operation{ID: 1, UnitID: u.ID}}
	if err := svc.runCreate(context.Background(), r, u, map[string]string{}, []*store.Instance{master}); err == nil {
		t.Fatal("runCreate succeeded; want an error when ACL accounts cannot be read")
	}
	if len(snd.sends) != 0 {
		t.Fatalf("%d commands sent to agents; no instance may be created without its ACL users", len(snd.sends))
	}
}
