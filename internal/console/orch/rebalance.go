package orch

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// 集群槽位自动再平衡（编排层）。
//
// 计划由 PlanRebalance 纯函数给出；执行时逐步调用 runReshard，因此每一步的归属校验、
// 逐批进度记录与失败处理与人工在线迁槽完全一致。同一部署单元的变更互斥由 unit_locks 完成。

// RebalanceSpec 是再平衡操作的持久化请求。恢复暂停的操作时据此重建执行闭包，
// 计划会在恢复时重新计算，因此已完成的步骤不会被重复执行。
type RebalanceSpec struct {
	Batch     uint32 `json:"batch"`
	Tolerance uint32 `json:"tolerance"`
}

// RebalancePreview 是 dry_run 的结果。Basis 说明槽位数据来源：dry_run 只读取已保存的实例状态，
// 不向 agent 下发任何指令；真正执行时会重新读取 CLUSTER NODES 并重算计划。
type RebalancePreview struct {
	UnitID    uint                   `json:"unit_id"`
	Basis     string                 `json:"basis"`
	Balanced  bool                   `json:"balanced"`
	Tolerance uint32                 `json:"tolerance"`
	Moved     uint32                 `json:"moved"`
	Nodes     []RebalancePreviewNode `json:"nodes"`
	Steps     []RebalancePreviewStep `json:"steps"`
}

// RebalancePreviewNode 是某个主节点再平衡前后的槽数。
type RebalancePreviewNode struct {
	InstanceID uint   `json:"instance_id"`
	Name       string `json:"name"`
	Before     uint32 `json:"before"`
	After      uint32 `json:"after"`
	Target     uint32 `json:"target"`
}

// RebalancePreviewStep 是计划中的一步：源 → 目标、槽区间与槽数。
type RebalancePreviewStep struct {
	Index            int        `json:"index"`
	SourceInstanceID uint       `json:"source_instance_id"`
	TargetInstanceID uint       `json:"target_instance_id"`
	Source           string     `json:"source"`
	Target           string     `json:"target"`
	Slots            []SlotSpan `json:"slots"`
	Count            uint32     `json:"count"`
}

// PreviewRebalance 基于已保存的实例状态计算再平衡计划，不下发任何指令。
func (s *Service) PreviewRebalance(unitID uint, tolerance uint32) (*RebalancePreview, error) {
	if err := s.checkRebalanceAllowed(tolerance); err != nil {
		return nil, err
	}
	if owner, err := s.Store.UnitLockOwner(unitID); err != nil {
		return nil, err
	} else if owner != 0 {
		return nil, store.ErrUnitBusy
	}
	_, masters, err := s.rebalanceMasters(unitID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.savedRebalanceNodes(masters)
	if err != nil {
		return nil, err
	}
	plan, err := PlanRebalance(nodes, tolerance)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*store.Instance, len(masters))
	for _, in := range masters {
		byID[in.ClusterNodeID] = in
	}
	return s.rebalancePreview(unitID, plan, byID, tolerance, "saved_state"), nil
}

// Rebalance 登记并在后台执行再平衡。计划在执行时由实时 CLUSTER NODES 重新计算。
func (s *Service) Rebalance(unitID uint, spec RebalanceSpec, by string) (*store.Operation, error) {
	if err := s.checkRebalanceAllowed(spec.Tolerance); err != nil {
		return nil, err
	}
	if _, _, err := s.rebalanceMasters(unitID); err != nil {
		return nil, err
	}
	if spec.Batch == 0 {
		spec.Batch = 100
	}
	if spec.Batch > 1000 {
		return nil, invalid("batch must be between 1 and 1000")
	}
	var run *opRun
	err := s.q().Transaction(func(tx *query.Query) error {
		var e error
		run, e = s.start(tx, unitID, "rebalance", spec, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, s.rebalanceJob(unitID, spec))
	return run.op, nil
}

func (s *Service) checkRebalanceAllowed(tolerance uint32) error {
	if s.License != nil && !s.License().Feature(license.FeatureReshard) {
		return fmt.Errorf("%w: online reshard is not licensed", ErrLicense)
	}
	if tolerance > RebalanceSlotCount {
		return invalid("tolerance must be between 0 and %d", RebalanceSlotCount)
	}
	return nil
}

// rebalanceJob 返回执行闭包。审批等待与进程恢复都通过它重建同一个执行流程。
func (s *Service) rebalanceJob(unitID uint, spec RebalanceSpec) func(context.Context, *opRun) error {
	return func(ctx context.Context, r *opRun) error {
		_, masters, err := s.rebalanceMasters(unitID)
		if err != nil {
			r.finish(store.OpFailed, err.Error())
			return err
		}
		nodes, byID, err := s.liveRebalanceNodes(ctx, masters)
		if err != nil {
			r.finish(store.OpFailed, err.Error())
			return err
		}
		plan, err := PlanRebalance(nodes, spec.Tolerance)
		if err != nil {
			r.finish(store.OpFailed, err.Error())
			return err
		}
		r.step("计算再平衡计划", masters[0].ID, "ok", fmt.Sprintf("balanced=%t steps=%d moved=%d", plan.Balanced, len(plan.Moves), plan.Moved))
		if err := s.runRebalancePlan(ctx, r, plan, byID, spec.Batch); err != nil {
			r.finish(store.OpPaused, err.Error())
			return err
		}
		r.finish(store.OpApplied, "")
		return nil
	}
}

// runRebalancePlan 按顺序执行计划。任一步失败即停止，已完成的步骤不回滚。
func (s *Service) runRebalancePlan(ctx context.Context, r *opRun, plan *RebalancePlan, byID map[string]*store.Instance, batch uint32) error {
	total := len(plan.Moves)
	for i, mv := range plan.Moves {
		from, to := byID[mv.Source], byID[mv.Target]
		if from == nil || to == nil {
			return fmt.Errorf("rebalance step %d/%d references an unknown master", i+1, total)
		}
		label := fmt.Sprintf("再平衡第 %d/%d 步 %s → %s", i+1, total, s.instName(from), s.instName(to))
		r.step(label+"开始", to.ID, "ok", fmt.Sprintf("slots=%d", mv.Count))
		slots := make([]*agentv1.SlotRange, 0, len(mv.Slots))
		for _, sp := range mv.Slots {
			slots = append(slots, &agentv1.SlotRange{Start: sp.Start, End: sp.End})
		}
		spec := ReshardSpec{SourceInstanceID: from.ID, TargetInstanceID: to.ID, Slots: slots, Batch: batch}
		if err := s.runReshard(ctx, r, from, to, spec); err != nil {
			r.step(label+"失败", from.ID, "failed", err.Error())
			return fmt.Errorf("stopped at rebalance step %d/%d (%s → %s): %w", i+1, total, s.instName(from), s.instName(to), err)
		}
		r.step(label+"完成", to.ID, "ok", "")
	}
	if total == 0 {
		r.step("槽位已平衡，无需迁移", 0, "skipped", "")
	}
	return nil
}

// rebalanceMasters 返回集群单元中的主节点（按实例 ID 排序）。非 cluster 形态直接拒绝。
func (s *Service) rebalanceMasters(unitID uint) (*store.Unit, []*store.Instance, error) {
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, nil, err
	}
	if u.Mode != store.ModeCluster {
		return nil, nil, invalid("rebalance is only available for cluster units")
	}
	insts, err := s.unitInstances(unitID)
	if err != nil {
		return nil, nil, err
	}
	var masters []*store.Instance
	for _, in := range insts {
		if in.Kind == "server" && isMaster(in) {
			masters = append(masters, in)
		}
	}
	if len(masters) == 0 {
		return nil, nil, invalid("cluster unit has no master instances")
	}
	return u, masters, nil
}

// savedRebalanceNodes 从已保存的实例状态（心跳上报）读取各主节点的槽位。
func (s *Service) savedRebalanceNodes(masters []*store.Instance) ([]RebalanceNode, error) {
	nodes := make([]RebalanceNode, 0, len(masters))
	for _, in := range masters {
		if in.ClusterNodeID == "" || in.ReportedAt == nil {
			return nil, invalid("%s has not reported its cluster state yet; wait for the next heartbeat", s.instName(in))
		}
		slots, err := parseSavedSlots(in.ClusterSlots)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.instName(in), err)
		}
		nodes = append(nodes, RebalanceNode{ID: in.ClusterNodeID, Slots: slots})
	}
	return nodes, nil
}

// liveRebalanceNodes 通过 CLUSTER NODES 实时读取槽位分布。执行前总是调用，不信任之前的计划。
func (s *Service) liveRebalanceNodes(ctx context.Context, masters []*store.Instance) ([]RebalanceNode, map[string]*store.Instance, error) {
	if len(masters) == 0 {
		return nil, nil, invalid("cluster unit has no master instances")
	}
	raw, err := s.clusterNodes(ctx, masters[0])
	if err != nil {
		return nil, nil, err
	}
	if err := clusterCheck(raw); err != nil {
		return nil, nil, fmt.Errorf("cluster slot view is inconsistent: %w", err)
	}
	byAddr := clusterNodeIDs(raw)
	nodes := make([]RebalanceNode, 0, len(masters))
	byID := make(map[string]*store.Instance, len(masters))
	for _, in := range masters {
		id, err := s.clusterInstanceNodeID(ctx, in, byAddr)
		if err != nil {
			return nil, nil, err
		}
		var spans []SlotSpan
		for _, rg := range clusterOwnedRanges(raw, id, RebalanceSlotCount) {
			spans = append(spans, SlotSpan{Start: rg.Start, End: rg.End})
		}
		nodes = append(nodes, RebalanceNode{ID: id, Slots: spans})
		byID[id] = in
	}
	return nodes, byID, nil
}

func (s *Service) rebalancePreview(unitID uint, plan *RebalancePlan, byID map[string]*store.Instance, tolerance uint32, basis string) *RebalancePreview {
	out := &RebalancePreview{UnitID: unitID, Basis: basis, Balanced: plan.Balanced, Tolerance: tolerance, Moved: plan.Moved,
		Nodes: make([]RebalancePreviewNode, 0, len(plan.Counts)), Steps: make([]RebalancePreviewStep, 0, len(plan.Moves))}
	for _, c := range plan.Counts {
		in := byID[c.ID]
		out.Nodes = append(out.Nodes, RebalancePreviewNode{InstanceID: in.ID, Name: s.instName(in), Before: c.Before, After: c.After, Target: c.Target})
	}
	for i, mv := range plan.Moves {
		from, to := byID[mv.Source], byID[mv.Target]
		out.Steps = append(out.Steps, RebalancePreviewStep{Index: i + 1, SourceInstanceID: from.ID, TargetInstanceID: to.ID,
			Source: s.instName(from), Target: s.instName(to), Slots: mv.Slots, Count: mv.Count})
	}
	return out
}

// parseSavedSlots 解析实例状态中保存的槽位字符串（以空格或逗号分隔的 "a" 或 "a-b"，忽略迁移中的 [..] 标记）。
func parseSavedSlots(raw string) ([]SlotSpan, error) {
	var out []SlotSpan
	tokens := strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' })
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "[") {
			continue
		}
		parts := strings.SplitN(tok, "-", 2)
		start, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			return nil, invalid("saved slot range %q is malformed", tok)
		}
		end := start
		if len(parts) == 2 {
			end, err = strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				return nil, invalid("saved slot range %q is malformed", tok)
			}
		}
		out = append(out, SlotSpan{Start: uint32(start), End: uint32(end)})
	}
	return out, nil
}
