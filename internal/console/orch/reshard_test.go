package orch

import "testing"

func TestClusterSlotOwner(t *testing.T) {
	const source = "0123456789012345678901234567890123456789"
	const target = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	raw := []byte(source + " 127.0.0.1:7000@17000 master - 0 0 1 connected 0-8191\n" + target + " 127.0.0.1:7001@17001 master - 0 0 2 connected 8192-16383\n")
	if got := clusterSlotOwner(raw, 0); got != source {
		t.Fatalf("slot 0 owner = %q, want %q", got, source)
	}
	if got := clusterSlotOwner(raw, 16383); got != target {
		t.Fatalf("slot 16383 owner = %q, want %q", got, target)
	}
	if err := clusterCheck(raw); err != nil {
		t.Fatalf("cluster check: %v", err)
	}
}

func TestClusterCheckRejectsGapAndOverlap(t *testing.T) {
	const source = "0123456789012345678901234567890123456789"
	const target = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	gap := []byte(source + " 127.0.0.1:7000@17000 master - 0 0 1 connected 0-100\n" + target + " 127.0.0.1:7001@17001 master - 0 0 2 connected 102-16383\n")
	if err := clusterCheck(gap); err == nil {
		t.Fatal("cluster check accepted an unassigned slot")
	}
	overlap := []byte(source + " 127.0.0.1:7000@17000 master - 0 0 1 connected 0-100\n" + target + " 127.0.0.1:7001@17001 master - 0 0 2 connected 100-16383\n")
	if err := clusterCheck(overlap); err == nil {
		t.Fatal("cluster check accepted an overlapping slot")
	}
}
