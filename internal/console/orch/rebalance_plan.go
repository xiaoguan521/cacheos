package orch

import "sort"

// 集群槽位再平衡的纯计算部分：只依据各主节点当前持有的槽集合生成迁移计划，不访问网络与数据库，
// 便于单测。执行由 rebalance.go 复用 runReshard 逐步完成。

// RebalanceSlotCount 是 Redis Cluster 的哈希槽总数（0..16383）。
const RebalanceSlotCount = 16384

// DefaultRebalanceTolerance 是默认容忍偏差（槽数）：所有主节点与目标值的偏差都不超过该值时视为已平衡。
const DefaultRebalanceTolerance uint32 = 0

// SlotSpan 是闭区间槽范围 [Start, End]。
type SlotSpan struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
}

// RebalanceNode 是一个主节点及其当前持有的槽区间（顺序任意，可以为空）。
type RebalanceNode struct {
	ID    string
	Slots []SlotSpan
}

// RebalanceMove 是一个迁移步骤：把 Slots 从 Source 迁往 Target，共 Count 个槽。
type RebalanceMove struct {
	Source string
	Target string
	Slots  []SlotSpan
	Count  uint32
}

// RebalanceCount 记录某节点再平衡前、后的槽数以及目标值。
type RebalanceCount struct {
	ID     string
	Before uint32
	After  uint32
	Target uint32
}

// RebalancePlan 是纯函数的输出。Moves 按执行顺序排列；Counts 按节点标识排序。
type RebalancePlan struct {
	Balanced bool
	Moves    []RebalanceMove
	Counts   []RebalanceCount
	Moved    uint32
}

// PlanRebalance 计算把槽尽量均分到各主节点的迁移计划。
//
// 规则：
//   - 目标：每个节点 floor(16384/N) 或 ceil(16384/N) 个槽；余数分给当前槽数较多的节点，
//     槽数相同则按节点标识升序，保证同一输入永远得到同一输出，且迁移量最小；
//   - 只从超出目标的节点迁出，只向不足的节点迁入；
//   - 每对（源，目标）只产生一个步骤，步骤内的槽区间尽量连续，源节点从区间尾部取槽，
//     使其余下的槽仍然是连续段；
//   - 所有节点偏差都不超过 tolerance 时返回空计划（Balanced=true）；
//   - 只有一个主节点、没有主节点、存在未分配槽、槽被重复持有或节点标识非法时返回错误。
func PlanRebalance(nodes []RebalanceNode, tolerance uint32) (*RebalancePlan, error) {
	n := len(nodes)
	if n == 0 {
		return nil, invalid("no master nodes to rebalance")
	}
	if n == 1 {
		return nil, invalid("only one master node, nothing to rebalance")
	}
	sorted := make([]RebalanceNode, n)
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for i, node := range sorted {
		if node.ID == "" {
			return nil, invalid("master node id is empty")
		}
		if i > 0 && sorted[i-1].ID == node.ID {
			return nil, invalid("duplicate master node id %q", node.ID)
		}
	}

	// 先把输入展开成槽归属表，同时检测越界、重复与遗漏；之后的计算只依赖归一化后的区间。
	owner := make([]int, RebalanceSlotCount)
	for i := range owner {
		owner[i] = -1
	}
	for i, node := range sorted {
		for _, sp := range node.Slots {
			if sp.Start > sp.End || sp.End >= RebalanceSlotCount {
				return nil, invalid("slot range %d-%d of %q must be within 0..16383", sp.Start, sp.End, node.ID)
			}
			for s := sp.Start; s <= sp.End; s++ {
				if owner[s] != -1 {
					return nil, invalid("slot %d is held by more than one master (%q and %q)", s, sorted[owner[s]].ID, node.ID)
				}
				owner[s] = i
			}
		}
	}
	owned := make([][]SlotSpan, n)
	for s := 0; s < RebalanceSlotCount; {
		if owner[s] == -1 {
			s++
			continue
		}
		idx := owner[s]
		start := s
		for s < RebalanceSlotCount && owner[s] == idx {
			s++
		}
		owned[idx] = append(owned[idx], SlotSpan{Start: uint32(start), End: uint32(s - 1)})
	}
	var unassigned int
	for _, o := range owner {
		if o == -1 {
			unassigned++
		}
	}
	if unassigned > 0 {
		return nil, invalid("%d slots are not assigned to any master in this cluster", unassigned)
	}

	cur := make([]uint32, n)
	for i := range owned {
		cur[i] = spanTotal(owned[i])
	}
	target := rebalanceTargets(cur)

	plan := &RebalancePlan{}
	var maxDev uint32
	for i := range sorted {
		if d := absDiff(cur[i], target[i]); d > maxDev {
			maxDev = d
		}
	}
	if maxDev <= tolerance {
		plan.Balanced = true
	} else {
		plan.Moves = buildRebalanceMoves(sorted, owned, cur, target)
		for _, mv := range plan.Moves {
			plan.Moved += mv.Count
		}
	}

	after := make([]uint32, n)
	for i := range sorted {
		after[i] = target[i]
	}
	if plan.Balanced {
		copy(after, cur)
	}
	plan.Counts = make([]RebalanceCount, n)
	for i, node := range sorted {
		plan.Counts[i] = RebalanceCount{ID: node.ID, Before: cur[i], After: after[i], Target: target[i]}
	}
	return plan, nil
}

// rebalanceTargets 计算每个节点的目标槽数。余数给当前槽数较多的节点（减少迁移），相同则按标识升序。
// nodes 必须已按标识升序排列，cur 与之一一对应。
func rebalanceTargets(cur []uint32) []uint32 {
	n := uint32(len(cur))
	base := uint32(RebalanceSlotCount) / n
	rem := uint32(RebalanceSlotCount) % n
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return cur[order[a]] > cur[order[b]]
	})
	target := make([]uint32, n)
	for i := range target {
		target[i] = base
	}
	for k := uint32(0); k < rem; k++ {
		target[order[k]]++
	}
	return target
}

// buildRebalanceMoves 按节点标识顺序，把每个超出目标的源节点的槽依次分给不足的目标节点。
func buildRebalanceMoves(nodes []RebalanceNode, owned [][]SlotSpan, cur, target []uint32) []RebalanceMove {
	n := len(nodes)
	excess := make([]uint32, n)
	need := make([]uint32, n)
	for i := 0; i < n; i++ {
		if cur[i] > target[i] {
			excess[i] = cur[i] - target[i]
		} else {
			need[i] = target[i] - cur[i]
		}
	}
	var moves []RebalanceMove
	for src := 0; src < n; src++ {
		for dst := 0; dst < n && excess[src] > 0; dst++ {
			if need[dst] == 0 {
				continue
			}
			amount := excess[src]
			if need[dst] < amount {
				amount = need[dst]
			}
			var taken []SlotSpan
			owned[src], taken = takeTail(owned[src], amount)
			excess[src] -= amount
			need[dst] -= amount
			moves = append(moves, RebalanceMove{
				Source: nodes[src].ID,
				Target: nodes[dst].ID,
				Slots:  taken,
				Count:  amount,
			})
		}
	}
	return moves
}

// takeTail 从区间列表尾部取走 n 个槽，返回剩余区间与取走的区间（升序、已合并相邻段）。
func takeTail(spans []SlotSpan, n uint32) ([]SlotSpan, []SlotSpan) {
	var taken []SlotSpan
	for n > 0 && len(spans) > 0 {
		last := &spans[len(spans)-1]
		size := last.End - last.Start + 1
		if size <= n {
			taken = append(taken, *last)
			n -= size
			spans = spans[:len(spans)-1]
			continue
		}
		taken = append(taken, SlotSpan{Start: last.End - n + 1, End: last.End})
		last.End -= n
		n = 0
	}
	for i, j := 0, len(taken)-1; i < j; i, j = i+1, j-1 {
		taken[i], taken[j] = taken[j], taken[i]
	}
	return spans, taken
}

func spanTotal(spans []SlotSpan) uint32 {
	var total uint32
	for _, sp := range spans {
		total += sp.End - sp.Start + 1
	}
	return total
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
