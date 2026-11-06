package orch

import (
	"errors"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func TestPickScaleHostPrefersUnusedZoneAndCapacity(t *testing.T) {
	s := &Service{PortMin: 7000, PortMax: 7002}
	loads := map[uint]*hostLoad{
		1: {h: store.Host{ID: 1, Name: "rack-a", Zone: "az-a", MemTotalBytes: 1 << 30, DiskFreeBytes: 1 << 30}, allocMB: 128, ports: map[uint32]bool{7000: true}},
		2: {h: store.Host{ID: 2, Name: "rack-b", Zone: "az-b", MemTotalBytes: 1 << 30, DiskFreeBytes: 1 << 30}, allocMB: 32, ports: map[uint32]bool{}},
		3: {h: store.Host{ID: 3, Name: "rack-c", Zone: "az-c", MemTotalBytes: 1 << 30, DiskFreeBytes: 10}, allocMB: 0, ports: map[uint32]bool{}},
	}

	got, err := s.pickScaleHost(loads, map[uint]bool{1: true}, map[string]bool{"az-a": true}, 64, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.h.ID != 2 {
		t.Fatalf("picked host %d, want host 2 in the unused zone with enough disk", got.h.ID)
	}
	if got.lastPort != 7000 || !got.ports[7000] {
		t.Fatalf("port allocation = %d, ports=%v; want 7000", got.lastPort, got.ports)
	}
}

func TestPickScaleHostRejectsAntiAffinityAndCapacity(t *testing.T) {
	s := &Service{PortMin: 7000, PortMax: 7000}
	loads := map[uint]*hostLoad{
		1: {h: store.Host{ID: 1, Name: "full", Zone: "az-a", MemTotalBytes: 256 << 20, DiskFreeBytes: 1 << 30}, allocMB: 200, ports: map[uint32]bool{}},
		2: {h: store.Host{ID: 2, Name: "small-disk", Zone: "az-b", MemTotalBytes: 1 << 30, DiskFreeBytes: 10}, allocMB: 0, ports: map[uint32]bool{}},
	}

	_, err := s.pickScaleHost(loads, map[uint]bool{2: true}, map[string]bool{"az-a": true}, 64, 100)
	if !errors.Is(err, ErrNoHosts) {
		t.Fatalf("error = %v, want ErrNoHosts", err)
	}
}

func TestBuildScalePlanAddsClusterShardBeforeReplicas(t *testing.T) {
	u := &store.Unit{ID: 7, Mode: store.ModeCluster, Shards: 3, Replicas: 1}
	insts := []*store.Instance{
		{ID: 1, UnitID: 7, Kind: "server", Shard: 0, Desired: "master"},
		{ID: 2, UnitID: 7, Kind: "server", Shard: 0, Desired: "replica"},
		{ID: 3, UnitID: 7, Kind: "server", Shard: 1, Desired: "master"},
		{ID: 4, UnitID: 7, Kind: "server", Shard: 1, Desired: "replica"},
		{ID: 5, UnitID: 7, Kind: "server", Shard: 2, Desired: "master"},
		{ID: 6, UnitID: 7, Kind: "server", Shard: 2, Desired: "replica"},
	}
	adds, removes := buildScalePlan(u, insts, 4, 1)
	if len(removes) != 0 {
		t.Fatalf("unexpected removals: %#v", removes)
	}
	if len(adds) != 2 || adds[0].Shard != 3 || adds[0].Desired != "master" || adds[1].Desired != "replica" {
		t.Fatalf("unexpected additions: %#v", adds)
	}
}

func TestBuildScalePlanRemovesReplicasAndWholeRemovedShards(t *testing.T) {
	u := &store.Unit{ID: 7, Mode: store.ModeCluster, Shards: 4, Replicas: 2}
	var insts []*store.Instance
	var id uint = 1
	for shard := uint32(0); shard < 4; shard++ {
		insts = append(insts, &store.Instance{ID: id, UnitID: 7, Kind: "server", Shard: shard, Desired: "master"})
		id++
		for replica := 0; replica < 2; replica++ {
			insts = append(insts, &store.Instance{ID: id, UnitID: 7, Kind: "server", Shard: shard, Desired: "replica"})
			id++
		}
	}
	adds, removes := buildScalePlan(u, insts, 3, 1)
	if len(adds) != 0 {
		t.Fatalf("unexpected additions: %#v", adds)
	}
	if len(removes) != 6 { // one replica from each kept shard + all of shard 3
		t.Fatalf("got %d removals, want 6: %#v", len(removes), removes)
	}
}

func TestClusterOwnedRangesHonorsLimitAndIgnoresTransientSlots(t *testing.T) {
	raw := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 10.0.0.1:7000@17000 master - 0 0 1 connected 0-99 200-299 [400->bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb]\n")
	ranges := clusterOwnedRanges(raw, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 120)
	if len(ranges) != 2 || ranges[0].Start != 0 || ranges[0].End != 99 || ranges[1].Start != 200 || ranges[1].End != 219 {
		t.Fatalf("unexpected ranges: %#v", ranges)
	}
}
