package orch

// 变更审批恢复：审批等待的操作只把请求参数和部署单元锁持久化到数据库，
// 不能把 Go 闭包持久化。控制台进程重启后，审批接口按 Operation.Kind + Request
// 重建同一个执行闭包，因此不会因为主备切换而把 PRECHECKED 操作永久卡死。

import (
	"context"
	"encoding/json"
	"fmt"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func (s *Service) rebuildPending(op *store.Operation) (func(context.Context, *opRun) error, error) {
	switch op.Kind {
	case "create":
		unit, err := store.First[store.Unit](s.Store, op.UnitID)
		if err != nil {
			return nil, err
		}
		params, err := s.revParams(unit.RevisionID)
		if err != nil {
			return nil, err
		}
		insts, err := s.unitInstances(op.UnitID)
		if err != nil {
			return nil, err
		}
		var spec UnitSpec
		if err := json.Unmarshal([]byte(op.Request), &spec); err != nil {
			return nil, fmt.Errorf("decode create operation: %w", err)
		}
		return func(ctx context.Context, r *opRun) error {
			return s.runCreate(ctx, r, unit, params, insts)
		}, nil

	case "params":
		var ch ParamChange
		if err := json.Unmarshal([]byte(op.Request), &ch); err != nil {
			return nil, fmt.Errorf("decode params operation: %w", err)
		}
		if op.Status == store.OpPaused {
			if !ch.Restart {
				return nil, fmt.Errorf("%w: paused parameter operation has no rolling restart", ErrUnsupported)
			}
			// Params are applied to every instance before rollingRestart can pause.
			// Replaying runParams would resend the revision with the wrong parent
			// revision, so recovery resumes only the durable restart portion.
			return func(ctx context.Context, r *opRun) error {
				if err := s.rollingRestart(ctx, r, op.UnitID, ch.Switchover); err != nil {
					r.finish(store.OpPaused, "parameters applied, rolling restart paused: "+err.Error())
					return err
				}
				r.finish(store.OpApplied, "")
				return nil
			}, nil
		}
		u, err := store.First[store.Unit](s.Store, op.UnitID)
		if err != nil {
			return nil, err
		}
		pv, next, err := s.PreviewParams(op.UnitID, ch)
		if err != nil {
			return nil, err
		}
		// The unit lock prevents another parameter operation from creating a
		// newer revision while this one waits for approval.
		revs, err := s.q().ParamRevision.WithContext(context.Background()).Where(s.q().ParamRevision.UnitID.Eq(op.UnitID)).Order(s.q().ParamRevision.Seq.Desc()).Limit(1).Find()
		if err != nil || len(revs) == 0 {
			if err == nil {
				err = fmt.Errorf("parameter revision not found")
			}
			return nil, err
		}
		targetMB, _, err := targetMemoryMB(next["maxmemory"], u.MemoryMB)
		if err != nil {
			return nil, err
		}
		rev := revs[0]
		return func(ctx context.Context, r *opRun) error {
			return s.runParams(ctx, r, op.UnitID, pv, rev, ch, targetMB)
		}, nil

	case "start", "stop", "restart":
		var req struct {
			Action     string `json:"action"`
			Switchover bool   `json:"switchover"`
		}
		if err := json.Unmarshal([]byte(op.Request), &req); err != nil {
			return nil, fmt.Errorf("decode action operation: %w", err)
		}
		var action agentv1.InstanceAction_Action
		switch req.Action {
		case "start":
			action = agentv1.InstanceAction_START
		case "stop":
			action = agentv1.InstanceAction_STOP
		case "restart":
			action = agentv1.InstanceAction_RESTART
		default:
			return nil, invalid("unknown persisted action %q", req.Action)
		}
		return func(ctx context.Context, r *opRun) error {
			return s.runAction(ctx, r, op.UnitID, action, req.Switchover)
		}, nil

	case "failover":
		return func(ctx context.Context, r *opRun) error {
			if err := s.failover(ctx, r, op.UnitID); err != nil {
				r.finish(store.OpFailed, err.Error())
				return err
			}
			r.finish(store.OpApplied, "")
			return nil
		}, nil

	case "delete":
		return func(ctx context.Context, r *opRun) error { return s.runDelete(ctx, r, op.UnitID) }, nil

	case "reshard":
		var spec ReshardSpec
		if err := json.Unmarshal([]byte(op.Request), &spec); err != nil {
			return nil, fmt.Errorf("decode reshard operation: %w", err)
		}
		from, err := store.First[store.Instance](s.Store, spec.SourceInstanceID)
		if err != nil {
			return nil, err
		}
		to, err := store.First[store.Instance](s.Store, spec.TargetInstanceID)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, r *opRun) error {
			if err := s.runReshard(ctx, r, from, to, spec); err != nil {
				r.finish(store.OpPaused, err.Error())
				return err
			}
			r.finish(store.OpApplied, "")
			return nil
		}, nil

	case "upgrade":
		req, err := decodeEngineUpgrade(op.Request)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, r *opRun) error {
			return s.runUpgrade(ctx, r, op.UnitID, req)
		}, nil

	case "scale":
		var req ScaleSpec
		if err := json.Unmarshal([]byte(op.Request), &req); err != nil {
			return nil, fmt.Errorf("decode scale operation: %w", err)
		}
		return func(ctx context.Context, r *opRun) error {
			return s.runScale(ctx, r, op.UnitID, req)
		}, nil

	case "sentinel":
		req, err := s.decodeSentinelOperation(op.Request)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, r *opRun) error {
			return s.runSentinel(ctx, r, op.UnitID, req)
		}, nil
	default:
		return nil, fmt.Errorf("%w: operation kind %q cannot be recovered", ErrUnsupported, op.Kind)
	}
}
