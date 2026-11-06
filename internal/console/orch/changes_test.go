package orch

import (
	"reflect"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func TestSafeAccountCategoriesDenyDangerousCommandsLast(t *testing.T) {
	got := safeAccountCategories([]string{"+@all", "-@dangerous"})
	want := []string{"+@all", "-@dangerous", "-@dangerous", "-flushall", "-flushdb", "-keys", "-debug", "-config"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dangerous deny rules = %v, want %v", got, want)
	}
}

func TestReplicaCaughtUpComparesAgainstMasterSnapshot(t *testing.T) {
	const target = 1000 // 主节点 repl_offset 快照
	if replicaCaughtUp(map[string]string{"replica_offset": "999"}, target) {
		t.Fatal("replica behind the master snapshot was accepted")
	}
	if !replicaCaughtUp(map[string]string{"replica_offset": "1000"}, target) {
		t.Fatal("replica at the master snapshot was rejected")
	}
	if !replicaCaughtUp(map[string]string{"replica_offset": "1500"}, target) {
		t.Fatal("replica past the snapshot (master kept writing) was rejected")
	}
	// 从节点自己的复制流偏移 repl_offset 不能代替已处理偏移
	if replicaCaughtUp(map[string]string{"repl_offset": "5000"}, target) {
		t.Fatal("replica's own stream offset was used as its processed offset")
	}
	if replicaCaughtUp(nil, target) || replicaCaughtUp(map[string]string{"replica_offset": "x"}, target) {
		t.Fatal("missing or malformed offset was accepted")
	}
}

func TestRebuildPendingOperationAfterProcessRestart(t *testing.T) {
	s := &Service{}
	op := &store.Operation{ID: 42, UnitID: 7, Kind: "failover", Request: `{}`}
	f, err := s.rebuildPending(op)
	if err != nil {
		t.Fatalf("rebuild pending operation: %v", err)
	}
	if f == nil {
		t.Fatal("rebuild returned a nil operation")
	}
}
