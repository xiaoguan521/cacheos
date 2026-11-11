package orch

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fullSpan 返回 [start, end] 的单段槽区间，便于构造测试输入。
func fullSpan(start, end uint32) []SlotSpan { return []SlotSpan{{Start: start, End: end}} }

// evenNodes 按 16384 槽近似均分给各节点（余数给前面的节点），仅用于构造起点。
func evenNodes(ids ...string) []RebalanceNode {
	n := uint32(len(ids))
	out := make([]RebalanceNode, 0, n)
	var start uint32
	for i, id := range ids {
		size := uint32(16384) / n
		if uint32(i) < uint32(16384)%n {
			size++
		}
		out = append(out, RebalanceNode{ID: id, Slots: fullSpan(start, start+size-1)})
		start += size
	}
	return out
}

// applyPlan 按顺序模拟执行计划并返回槽归属表。每一步都校验源节点确实持有这些槽。
func applyPlan(t *testing.T, nodes []RebalanceNode, plan *RebalancePlan) map[uint32]string {
	t.Helper()
	owner := map[uint32]string{}
	for _, n := range nodes {
		for _, sp := range n.Slots {
			for s := sp.Start; s <= sp.End; s++ {
				if prev, ok := owner[s]; ok {
					t.Fatalf("input slot %d held by both %s and %s", s, prev, n.ID)
				}
				owner[s] = n.ID
			}
		}
	}
	for i, mv := range plan.Moves {
		var moved uint32
		for _, sp := range mv.Slots {
			for s := sp.Start; s <= sp.End; s++ {
				if owner[s] != mv.Source {
					t.Fatalf("step %d moves slot %d from %s, but it is owned by %q", i+1, s, mv.Source, owner[s])
				}
				owner[s] = mv.Target
				moved++
			}
		}
		if moved != mv.Count {
			t.Fatalf("step %d Count=%d but spans cover %d slots", i+1, mv.Count, moved)
		}
	}
	return owner
}

// countsOf 校验归属表覆盖 16384 个槽且无遗漏，并统计每个节点的槽数。
func countsOf(t *testing.T, owner map[uint32]string) map[string]uint32 {
	t.Helper()
	if len(owner) != 16384 {
		t.Fatalf("total owned slots = %d, want 16384", len(owner))
	}
	out := map[string]uint32{}
	for s := uint32(0); s < 16384; s++ {
		id, ok := owner[s]
		if !ok {
			t.Fatalf("slot %d lost", s)
		}
		out[id]++
	}
	return out
}

func mustPlan(t *testing.T, nodes []RebalanceNode, tol uint32) *RebalancePlan {
	t.Helper()
	plan, err := PlanRebalance(nodes, tol)
	if err != nil {
		t.Fatalf("PlanRebalance: %v", err)
	}
	return plan
}

func countFor(plan *RebalancePlan, id string) RebalanceCount {
	for _, c := range plan.Counts {
		if c.ID == id {
			return c
		}
	}
	return RebalanceCount{}
}

func TestRebalanceThreeMastersPlusEmptyNode(t *testing.T) {
	// 3 个均衡主节点再加入 1 个空主节点：目标均为 4096。
	nodes := append(evenNodes("a", "b", "c"), RebalanceNode{ID: "d"})
	plan := mustPlan(t, nodes, 0)
	if plan.Balanced {
		t.Fatal("unbalanced cluster reported as balanced")
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if c := countFor(plan, id); c.Target != 4096 || c.After != 4096 {
			t.Fatalf("node %s target/after = %d/%d, want 4096", id, c.Target, c.After)
		}
	}
	if plan.Moved != 4096 {
		t.Fatalf("moved = %d, want 4096 (minimum movement)", plan.Moved)
	}
	for _, mv := range plan.Moves {
		if mv.Target != "d" {
			t.Fatalf("move targets %s, want only the new node d: %+v", mv.Target, mv)
		}
	}
	if len(plan.Moves) != 3 {
		t.Fatalf("got %d steps, want one per source (3): %+v", len(plan.Moves), plan.Moves)
	}
	counts := countsOf(t, applyPlan(t, nodes, plan))
	for _, id := range []string{"a", "b", "c", "d"} {
		if counts[id] != 4096 {
			t.Fatalf("after applying plan node %s has %d slots, want 4096", id, counts[id])
		}
	}
}

func TestRebalanceSixMastersTwoEmpty(t *testing.T) {
	// 6 个主节点其中 2 个为空：目标 16384/6 = 2730 余 4。
	nodes := append(evenNodes("n1", "n2", "n3", "n4"), RebalanceNode{ID: "n5"}, RebalanceNode{ID: "n6"})
	plan := mustPlan(t, nodes, 0)
	counts := countsOf(t, applyPlan(t, nodes, plan))
	var sum uint32
	for _, c := range plan.Counts {
		if c.Target != 2730 && c.Target != 2731 {
			t.Fatalf("node %s target %d is not floor/ceil of 16384/6", c.ID, c.Target)
		}
		if c.After != c.Target || counts[c.ID] != c.Target {
			t.Fatalf("node %s after=%d holds=%d target=%d", c.ID, c.After, counts[c.ID], c.Target)
		}
		sum += c.Target
	}
	if sum != 16384 {
		t.Fatalf("targets sum to %d", sum)
	}
	for _, mv := range plan.Moves {
		if mv.Source == "n5" || mv.Source == "n6" {
			t.Fatalf("empty node used as source: %+v", mv)
		}
		if mv.Target == "n1" || mv.Target == "n2" || mv.Target == "n3" || mv.Target == "n4" {
			t.Fatalf("balanced-or-excess node used as target: %+v", mv)
		}
	}
}

func TestRebalanceExtremeImbalance(t *testing.T) {
	nodes := []RebalanceNode{
		{ID: "hot", Slots: fullSpan(0, 16383)},
		{ID: "e1"}, {ID: "e2"}, {ID: "e3"},
	}
	plan := mustPlan(t, nodes, 0)
	counts := countsOf(t, applyPlan(t, nodes, plan))
	for _, id := range []string{"hot", "e1", "e2", "e3"} {
		if counts[id] != 4096 {
			t.Fatalf("node %s = %d, want 4096", id, counts[id])
		}
	}
	if plan.Moved != 3*4096 {
		t.Fatalf("moved = %d, want %d", plan.Moved, 3*4096)
	}
	if len(plan.Moves) != 3 {
		t.Fatalf("got %d steps, want 3: %+v", len(plan.Moves), plan.Moves)
	}
	for _, mv := range plan.Moves {
		if len(mv.Slots) != 1 {
			t.Fatalf("step should move one contiguous range: %+v", mv)
		}
	}
}

func TestRebalanceAlreadyBalancedReturnsEmptyPlan(t *testing.T) {
	plan := mustPlan(t, evenNodes("a", "b", "c", "d"), 0)
	if !plan.Balanced || len(plan.Moves) != 0 || plan.Moved != 0 {
		t.Fatalf("balanced cluster produced plan %+v", plan)
	}
	for _, c := range plan.Counts {
		if c.Before != c.After {
			t.Fatalf("balanced node %s changed %d -> %d", c.ID, c.Before, c.After)
		}
	}
}

func TestRebalanceTolerancePreventsMovesWithinThreshold(t *testing.T) {
	// 目标 8192：a 多 3 个，b 少 3 个。
	nodes := []RebalanceNode{
		{ID: "a", Slots: fullSpan(0, 8194)},
		{ID: "b", Slots: fullSpan(8195, 16383)},
	}
	if plan := mustPlan(t, nodes, 3); !plan.Balanced || len(plan.Moves) != 0 {
		t.Fatalf("deviation 3 within tolerance 3 should be balanced: %+v", plan)
	}
	plan := mustPlan(t, nodes, 2)
	if plan.Balanced || len(plan.Moves) == 0 {
		t.Fatalf("deviation 3 beyond tolerance 2 should produce a plan: %+v", plan)
	}
	counts := countsOf(t, applyPlan(t, nodes, plan))
	if counts["a"] != 8192 || counts["b"] != 8192 {
		t.Fatalf("plan result %v, want 8192 each", counts)
	}
}

func TestRebalanceDefaultToleranceIsExact(t *testing.T) {
	if DefaultRebalanceTolerance != 0 {
		t.Fatalf("default tolerance = %d, want 0", DefaultRebalanceTolerance)
	}
}

func TestRebalanceRemainderIsDeterministic(t *testing.T) {
	// 3 个节点：16384 = 3*5461 + 1，多出的 1 个目标槽必须落在确定的节点上。
	base := []RebalanceNode{
		{ID: "b", Slots: fullSpan(0, 8191)},
		{ID: "c", Slots: fullSpan(8192, 16383)},
		{ID: "a"},
	}
	first := mustPlan(t, base, 0)
	perms := [][]RebalanceNode{
		{base[0], base[1], base[2]},
		{base[2], base[0], base[1]},
		{base[1], base[2], base[0]},
	}
	for i := 0; i < 50; i++ {
		got := mustPlan(t, perms[i%len(perms)], 0)
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("run %d: plan differs for permuted input\nfirst=%+v\ngot=%+v", i, first, got)
		}
	}
	// 5 个节点：16384 = 5*3276 + 4。当前槽多的 v 先拿到余数，其余 3 个余数名额在
	// 槽数相同（3276）的节点中按标识升序分配：w、x、y；z 保持 3276。
	tie := []RebalanceNode{
		{ID: "x", Slots: fullSpan(0, 3275)},
		{ID: "y", Slots: fullSpan(3276, 6551)},
		{ID: "z", Slots: fullSpan(6552, 9827)},
		{ID: "w", Slots: fullSpan(9828, 13103)},
		{ID: "v", Slots: fullSpan(13104, 16383)},
	}
	var ceil []string
	for _, c := range mustPlan(t, tie, 0).Counts {
		if c.Target == 3277 {
			ceil = append(ceil, c.ID)
		}
	}
	if want := []string{"v", "w", "x", "y"}; !reflect.DeepEqual(ceil, want) {
		t.Fatalf("remainder holders = %v, want %v", ceil, want)
	}
}

func TestRebalanceMinimalMovementNoPointlessSwaps(t *testing.T) {
	// 目标 4096：b 已达标，不得与其他节点互相对调。
	nodes := []RebalanceNode{
		{ID: "a", Slots: fullSpan(0, 8191)},
		{ID: "b", Slots: fullSpan(8192, 12287)},
		{ID: "c", Slots: fullSpan(12288, 16383)},
		{ID: "d"},
	}
	plan := mustPlan(t, nodes, 0)
	for _, mv := range plan.Moves {
		if mv.Source == "b" || mv.Target == "b" {
			t.Fatalf("balanced node b participated in a move: %+v", mv)
		}
	}
	if plan.Moved != 4096 {
		t.Fatalf("moved %d slots, minimum is 4096", plan.Moved)
	}
}

func TestRebalanceStepsAreContiguous(t *testing.T) {
	plan := mustPlan(t, []RebalanceNode{{ID: "a", Slots: fullSpan(0, 16383)}, {ID: "b"}}, 0)
	if len(plan.Moves) != 1 || len(plan.Moves[0].Slots) != 1 {
		t.Fatalf("expected one contiguous step, got %+v", plan.Moves)
	}
	// 两节点各目标 8192，源节点从区间尾部取出，余下 0-8191 仍是连续段。
	if got := plan.Moves[0].Slots[0]; got.Start != 8192 || got.End != 16383 {
		t.Fatalf("moved range = %+v", got)
	}
}

func TestRebalanceAcceptsUnsortedAndSplitInput(t *testing.T) {
	nodes := []RebalanceNode{
		{ID: "a", Slots: []SlotSpan{{Start: 8000, End: 16383}, {Start: 0, End: 7999}}},
		{ID: "b"},
	}
	counts := countsOf(t, applyPlan(t, nodes, mustPlan(t, nodes, 0)))
	if counts["a"] != 8192 || counts["b"] != 8192 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestRebalanceCountsSortedByNodeID(t *testing.T) {
	nodes := []RebalanceNode{{ID: "z"}, {ID: "m", Slots: fullSpan(0, 16383)}, {ID: "a"}}
	plan := mustPlan(t, nodes, 0)
	ids := make([]string, 0, len(plan.Counts))
	for _, c := range plan.Counts {
		ids = append(ids, c.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("counts not sorted by node id: %v", ids)
	}
}

func TestRebalanceErrors(t *testing.T) {
	cases := []struct {
		name  string
		nodes []RebalanceNode
		want  string
	}{
		{"no masters", nil, "no master"},
		{"single master", []RebalanceNode{{ID: "a", Slots: fullSpan(0, 16383)}}, "only one master"},
		{"single empty master", []RebalanceNode{{ID: "a"}}, "only one master"},
		{"unassigned slots", []RebalanceNode{
			{ID: "a", Slots: fullSpan(0, 8190)},
			{ID: "b", Slots: fullSpan(8192, 16383)},
		}, "not assigned"},
		{"duplicate across nodes", []RebalanceNode{
			{ID: "a", Slots: fullSpan(0, 9000)},
			{ID: "b", Slots: fullSpan(9000, 16383)},
		}, "more than one"},
		{"duplicate within node", []RebalanceNode{
			{ID: "a", Slots: []SlotSpan{{Start: 0, End: 9000}, {Start: 9000, End: 9000}}},
			{ID: "b", Slots: fullSpan(9001, 16383)},
		}, "more than one"},
		{"range out of bounds", []RebalanceNode{
			{ID: "a", Slots: fullSpan(0, 16384)},
			{ID: "b"},
		}, "0..16383"},
		{"inverted range", []RebalanceNode{
			{ID: "a", Slots: []SlotSpan{{Start: 10, End: 5}}},
			{ID: "b"},
		}, "0..16383"},
		{"duplicate node id", []RebalanceNode{
			{ID: "a", Slots: fullSpan(0, 8191)},
			{ID: "a", Slots: fullSpan(8192, 16383)},
		}, "duplicate"},
		{"empty node id", []RebalanceNode{
			{ID: "", Slots: fullSpan(0, 8191)},
			{ID: "b", Slots: fullSpan(8192, 16383)},
		}, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanRebalance(tc.nodes, 0)
			if err == nil {
				t.Fatalf("expected error, got plan %+v", plan)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v should wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}
