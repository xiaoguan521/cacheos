package instance

import "testing"

func TestReplicationOffsetsSeparatesStreamAndProcessedOffsets(t *testing.T) {
	// 从节点：两个值分开返回，控制台只用 replicaOffset 与主节点比较
	stream, replica, isReplica, ok := replicationOffsets(map[string]string{
		"role":               "slave",
		"master_repl_offset": "1200",
		"slave_repl_offset":  "1199",
	})
	if !ok || !isReplica || stream != 1200 || replica != 1199 {
		t.Fatalf("replica: got (%d, %d, %v, %v)", stream, replica, isReplica, ok)
	}

	// 主节点：只有复制流偏移
	stream, _, isReplica, ok = replicationOffsets(map[string]string{"role": "master", "master_repl_offset": "5000"})
	if !ok || isReplica || stream != 5000 {
		t.Fatalf("master: got (%d, %v, %v)", stream, isReplica, ok)
	}

	// 只有已收到偏移、没有已处理偏移时不能当作从节点进度
	_, _, isReplica, ok = replicationOffsets(map[string]string{
		"master_repl_offset":     "1200",
		"slave_read_repl_offset": "1200",
	})
	if !ok || isReplica {
		t.Fatalf("read offset must not count as processed offset: isReplica=%v ok=%v", isReplica, ok)
	}

	if _, _, _, ok := replicationOffsets(map[string]string{}); ok {
		t.Fatal("accepted INFO without master_repl_offset")
	}
}
