package orch

import "testing"

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
