package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

// EngineUpgrade is the durable request for a data-plane rolling upgrade.
// The package itself remains in the console version library; each agent
// fetches it over the authenticated package stream and verifies it again
// before the root wrapper installs it.
type EngineUpgrade struct {
	Version    string          `json:"version"`
	Switchover bool            `json:"switchover"`
	Previous   map[uint]string `json:"previous,omitempty"` // host id -> current before the rollout
}

type engineUpgradeHost struct {
	host       *store.Host
	oldCurrent string
}

// UpgradeUnit performs the preflight for all hosts before creating the
// operation. This prevents a mixed-version rollout caused by discovering a
// missing architecture package halfway through the plan.
func (s *Service) UpgradeUnit(unitID uint, req EngineUpgrade, by string) (*store.Operation, error) {
	req.Version = strings.TrimSpace(req.Version)
	if !pkgsig.VersionRe.MatchString(req.Version) {
		return nil, invalid("invalid engine version %q", req.Version)
	}
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, err
	}
	if u.EngineVer == req.Version {
		return nil, invalid("unit is already on engine version %s", req.Version)
	}
	insts, err := s.unitInstances(unitID)
	if err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return nil, invalid("unit has no instances")
	}
	if err := s.preflightEngineUpgrade(req.Version, insts); err != nil {
		return nil, err
	}
	req.Previous = map[uint]string{}
	for _, in := range insts {
		if _, ok := req.Previous[in.HostID]; ok {
			continue
		}
		h, err := s.host(in.HostID)
		if err != nil {
			return nil, err
		}
		previous := strings.TrimSpace(h.EngineCurrent)
		if previous == "" {
			previous = u.EngineVer
		}
		req.Previous[in.HostID] = previous
	}

	var run *opRun
	err = s.q().Transaction(func(tx *query.Query) error {
		var e error
		run, e = s.start(tx, unitID, "upgrade", req, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error {
		return s.runUpgrade(ctx, r, unitID, req)
	})
	return run.op, nil
}

func (s *Service) preflightEngineUpgrade(version string, insts []*store.Instance) error {
	hosts := map[uint]*store.Host{}
	for _, in := range insts {
		h, err := s.host(in.HostID)
		if err != nil {
			return err
		}
		if _, ok := hosts[h.ID]; ok {
			continue
		}
		hosts[h.ID] = h
		if h.EngineCurrent == version || hasEngineVersion(h.EngineVersions, version) {
			continue
		}
		arch := engineArch(h.Arch)
		if arch == "" {
			return invalid("host %s reports unsupported architecture %q", h.Name, h.Arch)
		}
		if _, err := s.enginePackage(version, arch); err != nil {
			return invalid("host %s has no signed sycache package for %s/%s: %v", h.Name, version, arch, err)
		}
	}
	return nil
}

func engineArch(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return ""
	}
}

func hasEngineVersion(raw, version string) bool {
	for _, item := range strings.Split(raw, ",") {
		if strings.TrimSpace(item) == version {
			return true
		}
	}
	return false
}

func appendEngineVersion(raw, version string) string {
	if hasEngineVersion(raw, version) {
		return raw
	}
	items := make([]string, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	items = append(items, version)
	return strings.Join(items, ",")
}

func (s *Service) enginePackage(version, arch string) (*store.EnginePackage, error) {
	pq := s.q().EnginePackage
	return pq.WithContext(context.Background()).Where(pq.Name.Eq("sycache"), pq.Version.Eq(version), pq.Arch.Eq(arch)).First()
}

func (s *Service) runUpgrade(ctx context.Context, r *opRun, unitID uint, req EngineUpgrade) error {
	_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).Update(s.q().Unit.Status, "changing")
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		r.finish(store.OpFailed, err.Error())
		return err
	}
	insts, err := s.unitInstances(unitID)
	if err != nil {
		r.finish(store.OpFailed, err.Error())
		return err
	}
	if r.hasStepPrefix("回滚数据面版本 ") {
		var rollbackHosts []engineUpgradeHost
		seenRollback := map[uint]bool{}
		for _, in := range insts {
			if seenRollback[in.HostID] {
				continue
			}
			seenRollback[in.HostID] = true
			h, hostErr := s.host(in.HostID)
			if hostErr != nil {
				r.finish(store.OpPaused, "rollback host lookup failed: "+hostErr.Error())
				return hostErr
			}
			old := strings.TrimSpace(req.Previous[in.HostID])
			if old == "" {
				old = strings.TrimSpace(h.EngineCurrent)
				if old == "" {
					old = u.EngineVer
				}
			}
			rollbackHosts = append(rollbackHosts, engineUpgradeHost{host: h, oldCurrent: old})
		}
		return s.resumeUpgradeRollback(ctx, r, unitID, u.EngineVer, req.Switchover, rollbackHosts, req.Version)
	}
	// Preserve the first-seen instance order. A host can run several instances,
	// but its shared current symlink must be switched only once.
	seen := map[uint]bool{}
	var changed []engineUpgradeHost
	for _, in := range insts {
		if seen[in.HostID] {
			continue
		}
		seen[in.HostID] = true
		h, err := s.host(in.HostID)
		if err != nil {
			r.finish(store.OpFailed, err.Error())
			return err
		}
		old := strings.TrimSpace(req.Previous[in.HostID])
		if old == "" {
			old = strings.TrimSpace(h.EngineCurrent)
			if old == "" {
				old = u.EngineVer
			}
		}
		changed = append(changed, engineUpgradeHost{host: h, oldCurrent: old})
		stepName := "切换数据面版本 " + h.Name
		if r.hasSuccessfulStep(stepName, h.ID) {
			continue
		}
		cmd := &agentv1.SwitchVersion{Version: req.Version}
		if old != req.Version && !hasEngineVersion(h.EngineVersions, req.Version) {
			arch := engineArch(h.Arch)
			pkg, pkgErr := s.enginePackage(req.Version, arch)
			if pkgErr != nil {
				r.step(stepName, h.ID, "failed", pkgErr.Error())
				if rollbackErr := s.rollbackEngineHosts(ctx, r, changed, req.Version); rollbackErr != nil {
					_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).Update(s.q().Unit.Status, "degraded")
					r.finish(store.OpPaused, fmt.Sprintf("upgrade preflight failed: %v; rollback paused: %v", pkgErr, rollbackErr))
				} else {
					_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).Update(s.q().Unit.Status, "running")
					r.finish(store.OpRolledBack, pkgErr.Error())
				}
				return pkgErr
			}
			cmd.Arch, cmd.Sha256, cmd.VendorSignature = arch, pkg.SHA256, []byte(pkg.Sig)
		}
		if _, err := s.send(ctx, h.ID, cmd); err != nil {
			r.step(stepName, h.ID, "failed", err.Error())
			if rollbackErr := s.rollbackEngineHosts(ctx, r, changed, req.Version); rollbackErr != nil {
				_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).Update(s.q().Unit.Status, "degraded")
				r.finish(store.OpPaused, fmt.Sprintf("engine switch failed: %v; rollback paused: %v", err, rollbackErr))
			} else {
				_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).Update(s.q().Unit.Status, "running")
				r.finish(store.OpRolledBack, err.Error())
			}
			return err
		}
		versions := appendEngineVersion(h.EngineVersions, req.Version)
		hq := s.q().Host
		_, _ = hq.WithContext(ctx).Where(hq.ID.Eq(h.ID)).UpdateSimple(hq.EngineCurrent.Value(req.Version), hq.EngineVersions.Value(versions))
		h.EngineCurrent, h.EngineVersions = req.Version, versions
		r.step(stepName, h.ID, "ok", "")
	}

	if err := s.rollingRestart(ctx, r, unitID, req.Switchover); err != nil {
		uq := s.q().Unit
		_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unitID)).Update(uq.Status, "degraded")
		if rollbackErr := s.rollbackEngineHosts(ctx, r, changed, req.Version); rollbackErr != nil {
			r.finish(store.OpPaused, fmt.Sprintf("rolling restart failed: %v; rollback paused: %v", err, rollbackErr))
			return err
		}
		if rollbackRestartErr := s.rollingRestartNamed(ctx, r, unitID, req.Switchover, "回滚重启 ", "等待回滚复制恢复 "); rollbackRestartErr != nil {
			r.finish(store.OpPaused, fmt.Sprintf("rolling restart failed and rollback restart paused: %v", rollbackRestartErr))
			return rollbackRestartErr
		}
		_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unitID)).Update(uq.Status, "running")
		r.finish(store.OpRolledBack, "rolling restart failed; engine version rolled back: "+err.Error())
		return err
	}
	uq := s.q().Unit
	_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unitID)).UpdateSimple(uq.EngineVer.Value(req.Version), uq.Status.Value("running"))
	r.finish(store.OpApplied, "")
	return nil
}

func (s *Service) resumeUpgradeRollback(ctx context.Context, r *opRun, unitID uint, previousVersion string, switchover bool, hosts []engineUpgradeHost, target string) error {
	if err := s.rollbackEngineHosts(ctx, r, hosts, target); err != nil {
		r.finish(store.OpPaused, "rollback current symlinks paused: "+err.Error())
		return err
	}
	if err := s.rollingRestartNamed(ctx, r, unitID, switchover, "回滚重启 ", "等待回滚复制恢复 "); err != nil {
		r.finish(store.OpPaused, "rollback restart paused: "+err.Error())
		return err
	}
	uq := s.q().Unit
	_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unitID)).UpdateSimple(uq.EngineVer.Value(previousVersion), uq.Status.Value("running"))
	r.finish(store.OpRolledBack, "engine upgrade rollback completed")
	return nil
}

func (s *Service) rollbackEngineHosts(ctx context.Context, r *opRun, changed []engineUpgradeHost, target string) error {
	var firstErr error
	for i := len(changed) - 1; i >= 0; i-- {
		item := changed[i]
		if item.oldCurrent == "" || item.oldCurrent == target {
			continue
		}
		name := "回滚数据面版本 " + item.host.Name
		if r.hasSuccessfulStep(name, item.host.ID) {
			continue
		}
		if _, err := s.send(ctx, item.host.ID, &agentv1.SwitchVersion{Version: item.oldCurrent}); err != nil {
			r.step(name, item.host.ID, "failed", err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		hq := s.q().Host
		_, _ = hq.WithContext(ctx).Where(hq.ID.Eq(item.host.ID)).Update(hq.EngineCurrent, item.oldCurrent)
		item.host.EngineCurrent = item.oldCurrent
		r.step(name, item.host.ID, "rolled_back", "")
	}
	return firstErr
}

// decodeEngineUpgrade is kept small so recovery rejects malformed durable
// requests before reacquiring the unit lock.
func decodeEngineUpgrade(raw string) (EngineUpgrade, error) {
	var req EngineUpgrade
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return req, fmt.Errorf("decode upgrade operation: %w", err)
	}
	if !pkgsig.VersionRe.MatchString(strings.TrimSpace(req.Version)) {
		return req, invalid("invalid persisted engine version %q", req.Version)
	}
	req.Version = strings.TrimSpace(req.Version)
	for hostID, previous := range req.Previous {
		if hostID == 0 || (previous != "" && !pkgsig.VersionRe.MatchString(strings.TrimSpace(previous))) {
			return req, invalid("invalid persisted previous engine version for host %d", hostID)
		}
	}
	return req, nil
}
