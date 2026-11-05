package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
)

// 生成的查询在 SQLite 与 PostgreSQL 上都可用（类型安全的条件、排序、事务）。
func TestGeneratedQueries(t *testing.T) {
	st := storetest.Open(t)
	q := query.Use(st.DB)
	ctx := context.Background()
	h := &store.Host{Name: "cache-1", Status: "online"}
	if err := q.Host.WithContext(ctx).Create(h); err != nil {
		t.Fatal(err)
	}
	u := &store.Unit{Name: "orders", Mode: store.ModeReplication, Status: "creating"}
	if err := q.Transaction(func(tx *query.Query) error {
		if err := tx.Unit.WithContext(ctx).Create(u); err != nil {
			return err
		}
		for i, role := range []string{"master", "replica"} {
			if err := tx.Instance.WithContext(ctx).Create(&store.Instance{UnitID: u.ID, HostID: h.ID, Port: uint32(7001 + i), Kind: "server", Desired: role}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	in := q.Instance
	list, err := in.WithContext(ctx).Where(in.UnitID.Eq(u.ID)).Order(in.Port.Desc()).Find()
	if err != nil || len(list) != 2 || list[0].Port != 7002 {
		t.Fatalf("instances: %+v %v", list, err)
	}
	// 同一主机同一端口只能有一个实例
	if err := in.WithContext(ctx).Create(&store.Instance{UnitID: u.ID, HostID: h.ID, Port: 7001, Kind: "server"}); err == nil {
		t.Fatal("duplicate host/port accepted")
	}
	// 用户输入作为参数绑定，不会成为 SQL 的一部分
	n, err := q.Unit.WithContext(ctx).Where(q.Unit.Name.Eq("orders' OR '1'='1")).Count()
	if err != nil || n != 0 {
		t.Fatalf("injection-shaped value matched: %d %v", n, err)
	}
}

func TestUnitLock(t *testing.T) {
	st := storetest.Open(t)
	if err := st.LockUnit(1, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.LockUnit(1, 11); !errors.Is(err, store.ErrUnitBusy) {
		t.Fatalf("second lock: %v", err)
	}
	if owner, _ := st.UnitLockOwner(1); owner != 10 {
		t.Fatalf("owner %d", owner)
	}
	// 只有持有者能释放
	_ = st.UnlockUnit(1, 11)
	if owner, _ := st.UnitLockOwner(1); owner != 10 {
		t.Fatal("non-owner released the lock")
	}
	if err := st.UnlockUnit(1, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.LockUnit(1, 11); err != nil {
		t.Fatalf("relock: %v", err)
	}
}

func TestNextRevisionID(t *testing.T) {
	st := storetest.Open(t)
	id, err := store.NextRevisionID(st.DB)
	if err != nil || id != "p00000001" {
		t.Fatalf("%s %v", id, err)
	}
	if err := st.DB.Create(&store.ParamRevision{ID: id, UnitID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	id, _ = store.NextRevisionID(st.DB)
	if id != "p00000002" {
		t.Fatal(id)
	}
}
