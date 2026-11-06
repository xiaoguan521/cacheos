package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// ReshardSpec 描述一个受控在线迁槽计划。调用方可以一次提交多个槽区间，
// 编排器仍按槽、按批串行执行，任意时候都能从最近一步暂停。
type ReshardSpec struct {
	SourceInstanceID uint                 `json:"source_instance_id"`
	TargetInstanceID uint                 `json:"target_instance_id"`
	Slots            []*agentv1.SlotRange `json:"slots"`
	Batch            uint32               `json:"batch"`
}

func (s *Service) Reshard(unitID uint, spec ReshardSpec, by string) (*store.Operation, error) {
	if s.License != nil && !s.License().Feature(license.FeatureReshard) {
		return nil, fmt.Errorf("%w: online reshard is not licensed", ErrLicense)
	}
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, err
	}
	if u.Mode != store.ModeCluster {
		return nil, invalid("online reshard is only available for cluster units")
	}
	if spec.SourceInstanceID == 0 || spec.TargetInstanceID == 0 || spec.SourceInstanceID == spec.TargetInstanceID {
		return nil, invalid("source and target instances must be different")
	}
	if len(spec.Slots) == 0 {
		return nil, invalid("at least one slot range is required")
	}
	if spec.Batch == 0 {
		spec.Batch = 100
	}
	if spec.Batch > 1000 {
		return nil, invalid("batch must be between 1 and 1000")
	}
	for _, r := range spec.Slots {
		if r == nil || r.Start > r.End || r.End >= 16384 {
			return nil, invalid("slot range must be within 0..16383")
		}
	}
	from, err := store.First[store.Instance](s.Store, spec.SourceInstanceID)
	if err != nil || from.UnitID != unitID || from.Kind != "server" || !isMaster(from) {
		return nil, invalid("source instance must be a master in this cluster")
	}
	to, err := store.First[store.Instance](s.Store, spec.TargetInstanceID)
	if err != nil || to.UnitID != unitID || to.Kind != "server" || !isMaster(to) {
		return nil, invalid("target instance must be a master in this cluster")
	}
	var run *opRun
	err = s.q().Transaction(func(tx *query.Query) error {
		var e error
		run, e = s.start(tx, unitID, "reshard", spec, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error {
		if err := s.runReshard(ctx, r, from, to, spec); err != nil {
			r.finish(store.OpPaused, err.Error())
			return err
		}
		r.finish(store.OpApplied, "")
		return nil
	})
	return run.op, nil
}

func (s *Service) runReshard(ctx context.Context, r *opRun, from, to *store.Instance, spec ReshardSpec) error {
	peerUser, peerPassword, err := s.clusterMigrationCredentials(from.UnitID)
	if err != nil {
		return err
	}
	fromHost, err := s.host(from.HostID)
	if err != nil {
		return err
	}
	toHost, err := s.host(to.HostID)
	if err != nil {
		return err
	}
	nodesRes, err := s.send(ctx, from.HostID, &agentv1.ClusterOp{Port: from.Port, Op: agentv1.ClusterOp_NODES})
	if err != nil {
		return err
	}
	nodes := clusterNodeIDs(nodesRes.Payload)
	fromID := from.ClusterNodeID
	toID := to.ClusterNodeID
	if fromID == "" {
		fromID = nodes[clusterAddress(fromHost, from.Port)]
	}
	if toID == "" {
		toID = nodes[clusterAddress(toHost, to.Port)]
	}
	if fromID == "" || toID == "" {
		return errors.New("cluster node IDs are not available; wait for cluster convergence")
	}
	for _, rg := range spec.Slots {
		for slot := rg.Start; slot <= rg.End; slot++ {
			// A resumed operation is idempotent at the slot boundary: if the
			// durable cluster view already assigns this slot to the target, the
			// migration and ownership steps were completed before the pause.
			if owner := clusterSlotOwner(nodesRes.Payload, slot); owner == toID {
				r.step(fmt.Sprintf("槽 %d 已完成", slot), to.ID, "skipped", "target already owns the slot")
				continue
			}
			if owner := clusterSlotOwner(nodesRes.Payload, slot); owner != fromID {
				return fmt.Errorf("slot %d is owned by %q, expected source %s", slot, owner, fromID)
			}
			// The target enters IMPORTING before the source enters MIGRATING.
			if _, err := s.send(ctx, to.HostID, &agentv1.ClusterOp{Port: to.Port, Op: agentv1.ClusterOp_SETSLOT_IMPORTING, Slot: slot, NodeId: fromID}); err != nil {
				return fmt.Errorf("slot %d importing: %w", slot, err)
			}
			if _, err := s.send(ctx, from.HostID, &agentv1.ClusterOp{Port: from.Port, Op: agentv1.ClusterOp_SETSLOT_MIGRATING, Slot: slot, NodeId: toID}); err != nil {
				return fmt.Errorf("slot %d migrating: %w", slot, err)
			}
			for batchNo := 0; batchNo < 10000; batchNo++ {
				res, err := s.send(ctx, from.HostID, &agentv1.ClusterOp{Port: from.Port, Op: agentv1.ClusterOp_MIGRATE_SLOT, PeerHost: hostAddress(toHost), PeerPort: to.Port, PeerUser: peerUser, PeerPassword: peerPassword, Slot: slot, Batch: spec.Batch, TimeoutMs: 10000})
				if err != nil {
					return fmt.Errorf("slot %d batch %d: %w", slot, batchNo+1, err)
				}
				migrated := migratedCount(res.Payload)
				r.step(fmt.Sprintf("迁移槽 %d 批次 %d", slot, batchNo+1), from.ID, "ok", fmt.Sprintf("migrated=%d", migrated))
				if err := s.verifySlotProgress(ctx, from.HostID, from.Port, slot, fromID, toID); err != nil {
					return fmt.Errorf("slot %d batch %d cluster check: %w", slot, batchNo+1, err)
				}
				if migrated == 0 {
					break
				}
				if batchNo == 9999 {
					return fmt.Errorf("slot %d did not drain after 10000 batches", slot)
				}
			}
			if _, err := s.send(ctx, to.HostID, &agentv1.ClusterOp{Port: to.Port, Op: agentv1.ClusterOp_SETSLOT_NODE, Slot: slot, NodeId: toID}); err != nil {
				return fmt.Errorf("slot %d target ownership: %w", slot, err)
			}
			if _, err := s.send(ctx, from.HostID, &agentv1.ClusterOp{Port: from.Port, Op: agentv1.ClusterOp_SETSLOT_NODE, Slot: slot, NodeId: toID}); err != nil {
				return fmt.Errorf("slot %d source ownership: %w", slot, err)
			}
			nodesRes, err = s.send(ctx, to.HostID, &agentv1.ClusterOp{Port: to.Port, Op: agentv1.ClusterOp_NODES})
			if err != nil {
				return fmt.Errorf("slot %d refresh cluster topology: %w", slot, err)
			}
		}
	}
	if err := verifyClusterCheck(ctx, s, to.HostID, to.Port, spec.Slots, toID); err != nil {
		return err
	}
	// A fresh NODES result is saved in the operation steps, while heartbeats
	// update the durable cluster_slots/cluster_node_id fields shortly after.
	r.step("在线迁槽完成", to.ID, "ok", fmt.Sprintf("source=%d target=%d", from.ID, to.ID))
	return nil
}

func (s *Service) clusterMigrationCredentials(unitID uint) (string, string, error) {
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return "", "", err
	}
	if u.MasterAuth == "" {
		return "", "", nil
	}
	if s.Box == nil {
		return "", "", errors.New("cluster migration credentials are unavailable")
	}
	password, err := s.Box.Open(u.MasterAuth)
	if err != nil {
		return "", "", fmt.Errorf("open cluster migration credentials: %w", err)
	}
	return instance.UserCluster, password, nil
}

// clusterSlotOwner parses the normal slot ownership fields in CLUSTER NODES.
// Importing/migrating transient entries are bracketed and deliberately ignored;
// the normal owner remains the source until SETSLOT NODE commits the move.
func clusterSlotOwner(raw []byte, slot uint32) string {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || len(fields[0]) != 40 {
			continue
		}
		for _, token := range fields[8:] {
			if strings.HasPrefix(token, "[") {
				continue
			}
			parts := strings.SplitN(token, "-", 2)
			start, err := strconv.ParseUint(parts[0], 10, 32)
			if err != nil {
				continue
			}
			end := start
			if len(parts) == 2 {
				end, err = strconv.ParseUint(parts[1], 10, 32)
				if err != nil {
					continue
				}
			}
			if uint64(start) <= uint64(slot) && uint64(slot) <= uint64(end) {
				return fields[0]
			}
		}
	}
	return ""
}

func clusterCheck(raw []byte) error {
	owners := make(map[uint32]string, 16384)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || len(fields[0]) != 40 {
			continue
		}
		for _, token := range fields[8:] {
			if strings.HasPrefix(token, "[") {
				continue
			}
			parts := strings.SplitN(token, "-", 2)
			start, err := strconv.ParseUint(parts[0], 10, 32)
			if err != nil || start > 16383 {
				continue
			}
			end := start
			if len(parts) == 2 {
				end, err = strconv.ParseUint(parts[1], 10, 32)
				if err != nil || end > 16383 || end < start {
					continue
				}
			}
			for slot := uint32(start); slot <= uint32(end); slot++ {
				if old := owners[slot]; old != "" {
					return fmt.Errorf("slot %d is listed more than once (%s and %s)", slot, old, fields[0])
				}
				owners[slot] = fields[0]
			}
		}
	}
	if len(owners) != 16384 {
		return fmt.Errorf("cluster has %d of 16384 assigned slots", len(owners))
	}
	return nil
}

func (s *Service) verifySlotProgress(ctx context.Context, hostID uint, port uint32, slot uint32, sourceID, targetID string) error {
	res, err := s.send(ctx, hostID, &agentv1.ClusterOp{Port: port, Op: agentv1.ClusterOp_NODES})
	if err != nil {
		return err
	}
	owner := clusterSlotOwner(res.Payload, slot)
	if owner != sourceID && owner != targetID {
		return fmt.Errorf("slot %d owner %q is neither source nor target", slot, owner)
	}
	return nil
}

func verifyClusterCheck(ctx context.Context, s *Service, hostID uint, port uint32, ranges []*agentv1.SlotRange, targetID string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err := s.send(ctx, hostID, &agentv1.ClusterOp{Port: port, Op: agentv1.ClusterOp_NODES})
		if err == nil {
			if err = clusterCheck(res.Payload); err == nil {
				for _, rg := range ranges {
					for slot := rg.Start; slot <= rg.End; slot++ {
						if owner := clusterSlotOwner(res.Payload, slot); owner != targetID {
							err = fmt.Errorf("slot %d is owned by %q, expected target %s", slot, owner, targetID)
							break
						}
					}
					if err != nil {
						break
					}
				}
			}
		}
		if err == nil {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("cluster check did not converge within 10s")
}

func hostAddress(h *store.Host) string {
	if h.Address != "" {
		return h.Address
	}
	return h.Hostname
}

func migratedCount(payload []byte) int {
	var v struct {
		Migrated int `json:"migrated"`
	}
	if json.Unmarshal(payload, &v) != nil {
		return 0
	}
	return v.Migrated
}
