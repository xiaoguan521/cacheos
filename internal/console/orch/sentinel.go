package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// SentinelChange is the audited control-plane subset of Sentinel commands.
// Values are validated here and again by the agent before reaching Redis.
type SentinelChange struct {
	Operation  string            `json:"operation"` // monitor | remove | set | reset
	MasterName string            `json:"master_name"`
	MasterHost string            `json:"master_host"`
	MasterPort uint32            `json:"master_port"`
	Quorum     uint32            `json:"quorum"`
	Options    map[string]string `json:"options,omitempty"`
	AuthPass   string            `json:"auth_pass,omitempty"`
}

func (s *Service) SentinelOp(unitID uint, change SentinelChange, by string) (*store.Operation, error) {
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, err
	}
	if u.Mode != store.ModeSentinel {
		return nil, invalid("sentinel management is only available for sentinel units")
	}
	if s.License != nil && !s.License().Feature(license.FeatureSentinel) {
		return nil, fmt.Errorf("%w: sentinel management is not licensed", ErrLicense)
	}
	change.Operation = strings.ToLower(strings.TrimSpace(change.Operation))
	switch change.Operation {
	case "monitor", "remove", "set", "reset":
	default:
		return nil, invalid("unknown sentinel operation %q", change.Operation)
	}
	if change.MasterName == "" {
		change.MasterName = u.SentinelName
	}
	if change.MasterName == "" || strings.ContainsAny(change.MasterName, " \t\r\n\"'\\") {
		return nil, invalid("invalid sentinel master name")
	}
	if change.AuthPass != "" {
		return nil, invalid("auth_pass cannot be stored in a durable console operation; rotate it through the encrypted instance configuration")
	}
	allowed := map[string]bool{"down-after-milliseconds": true, "failover-timeout": true, "parallel-syncs": true, "auth-user": true, "auth-pass": true}
	for key, value := range change.Options {
		if key == "auth-pass" {
			return nil, invalid("auth-pass cannot be stored in a durable console operation")
		}
		if !allowed[key] || strings.ContainsAny(value, "\r\n") {
			return nil, invalid("sentinel option %q is not allowed", key)
		}
	}
	if change.Operation == "set" && len(change.Options) == 0 {
		return nil, invalid("sentinel SET requires at least one option")
	}
	if change.Operation == "monitor" {
		if change.MasterPort == 0 {
			insts, findErr := s.unitInstances(unitID)
			if findErr != nil {
				return nil, findErr
			}
			for _, in := range insts {
				if in.Kind == "server" && isMaster(in) {
					h, hostErr := s.host(in.HostID)
					if hostErr != nil {
						return nil, hostErr
					}
					change.MasterHost, change.MasterPort = hostAddress(h), in.Port
					break
				}
			}
		}
		if change.MasterHost == "" || change.MasterPort == 0 || change.Quorum == 0 {
			change.Quorum = u.Quorum
		}
		if change.MasterHost == "" || change.MasterPort == 0 || change.Quorum == 0 {
			return nil, invalid("monitor requires a master host, port and quorum")
		}
	}
	var run *opRun
	err = s.q().Transaction(func(tx *query.Query) error {
		var e error
		run, e = s.start(tx, unitID, "sentinel", change, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error { return s.runSentinel(ctx, r, unitID, change) })
	return run.op, nil
}

func (s *Service) runSentinel(ctx context.Context, r *opRun, unitID uint, change SentinelChange) error {
	insts, err := s.unitInstances(unitID)
	if err != nil {
		r.finish(store.OpPaused, err.Error())
		return err
	}
	op := agentv1.SentinelOp{MasterName: change.MasterName, MasterHost: change.MasterHost, MasterPort: change.MasterPort, Quorum: change.Quorum, Options: change.Options, AuthPass: change.AuthPass}
	switch change.Operation {
	case "monitor":
		op.Op = agentv1.SentinelOp_MONITOR
	case "remove":
		op.Op = agentv1.SentinelOp_REMOVE
	case "set":
		op.Op = agentv1.SentinelOp_SET
	case "reset":
		op.Op = agentv1.SentinelOp_RESET
	}
	for _, in := range insts {
		if in.Kind != "sentinel" {
			continue
		}
		step := "哨兵 " + change.Operation + " " + s.instName(in)
		if r.hasSuccessfulStep(step, in.ID) {
			continue
		}
		op.Port = in.Port
		res, sendErr := s.send(ctx, in.HostID, &op)
		if sendErr != nil {
			r.step(step, in.ID, "failed", sendErr.Error())
			r.finish(store.OpPaused, sendErr.Error())
			return sendErr
		}
		s.saveStatus(in, res.GetInstance())
		r.step(step, in.ID, "ok", "")
	}
	r.finish(store.OpApplied, "")
	return nil
}

func (s *Service) decodeSentinelOperation(raw string) (SentinelChange, error) {
	var change SentinelChange
	if err := json.Unmarshal([]byte(raw), &change); err != nil {
		return change, fmt.Errorf("decode sentinel operation: %w", err)
	}
	if change.Operation == "" {
		return change, errors.New("persisted sentinel operation is empty")
	}
	return change, nil
}
