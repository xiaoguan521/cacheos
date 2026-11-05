package orch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
)

// ParamChange 是一次参数变更请求。
type ParamChange struct {
	Set     map[string]string `json:"set"`     // 新值
	Reset   []string          `json:"reset"`   // 恢复默认值
	Note    string            `json:"note"`    // 变更说明（必填）
	Restart bool              `json:"restart"` // 有需重启参数时是否立即滚动重启；否则实例停在 pending_restart
	// Switchover 滚动重启到主节点时先做协调切换（从节点追平后互换角色），避免主节点重启期间不可写
	Switchover bool `json:"switchover"`
}

// Preview 是变更预检结果（规格书 6.3 第 2 步：diff、可热改 / 需重启分组）。
type Preview struct {
	Revision string            `json:"base_revision"`
	Changed  map[string]string `json:"changed"`
	Old      map[string]string `json:"old"`
	Hot      []string          `json:"hot"`
	Restart  []string          `json:"restart"`
}

func (s *Service) revParams(id string) (map[string]string, error) {
	var rev store.ParamRevision
	if err := s.Store.DB.Where("id = ?", id).First(&rev).Error; err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal([]byte(rev.Params), &m)
}

// PreviewParams 计算变更后的参数与差异，不做任何改动。
func (s *Service) PreviewParams(unitID uint, ch ParamChange) (*Preview, map[string]string, error) {
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, nil, err
	}
	old, err := s.revParams(u.RevisionID)
	if err != nil {
		return nil, nil, err
	}
	next := map[string]string{}
	for k, v := range old {
		next[k] = v
	}
	set, err := paramcat.NormalizeAll(ch.Set)
	if err != nil {
		return nil, nil, invalid("%v", err)
	}
	for k, v := range set {
		if k == "maxmemory" {
			return nil, nil, invalid("maxmemory follows the unit memory size; resize the unit instead")
		}
		next[k] = v
	}
	for _, k := range ch.Reset {
		p, ok := paramcat.Lookup(k)
		if !ok {
			return nil, nil, invalid("%s: %v", k, paramcat.ErrUnknown)
		}
		if p.Name == "maxmemory" {
			return nil, nil, invalid("maxmemory cannot be reset")
		}
		delete(next, p.Name)
	}
	changed, hot, restart := paramcat.Diff(old, next)
	pv := &Preview{Revision: u.RevisionID, Changed: changed, Old: map[string]string{}, Hot: hot, Restart: restart}
	for k := range changed {
		if v, ok := old[k]; ok {
			pv.Old[k] = v
		} else if p, ok := paramcat.Lookup(k); ok {
			pv.Old[k] = p.Default
		}
	}
	return pv, next, nil
}

func (s *Service) unitInstances(unitID uint) ([]*store.Instance, error) {
	var insts []*store.Instance
	err := s.Store.DB.Where("unit_id = ?", unitID).Order("id").Find(&insts).Error
	return insts, err
}

// rollingOrder：从节点在前、主节点最后（滚动变更时始终保留可写的主节点直到最后一步）。
func rollingOrder(insts []*store.Instance) []*store.Instance {
	out := append([]*store.Instance(nil), insts...)
	sort.SliceStable(out, func(i, j int) bool { return isMaster(out[j]) && !isMaster(out[i]) })
	return out
}

func isMaster(in *store.Instance) bool {
	if in.Role != "" {
		return in.Role == "master"
	}
	return in.Desired == "master"
}

// ApplyParams 生成新参数版本并逐实例下发（从节点在前）。任一实例失败：已改的实例回滚到原版本，变更记为 ROLLED_BACK。
func (s *Service) ApplyParams(unitID uint, ch ParamChange, by string) (*store.Operation, error) {
	if strings.TrimSpace(ch.Note) == "" {
		return nil, invalid("note is required")
	}
	pv, next, err := s.PreviewParams(unitID, ch)
	if err != nil {
		return nil, err
	}
	if len(pv.Changed) == 0 {
		return nil, ErrNoChanges
	}
	if err := s.licenseCheck(0, 0); err != nil {
		return nil, err
	}
	var run *opRun
	var rev *store.ParamRevision
	err = s.Store.DB.Transaction(func(tx *gorm.DB) error {
		var e error
		if run, e = s.start(tx, unitID, "params", ch, by); e != nil {
			return e
		}
		rev, e = s.newRevision(tx, unitID, pv.Revision, next, ch.Note, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error { return s.runParams(ctx, r, unitID, pv, rev, ch) })
	return run.op, nil
}

func (s *Service) runParams(ctx context.Context, r *opRun, unitID uint, pv *Preview, rev *store.ParamRevision, ch ParamChange) error {
	insts, err := s.unitInstances(unitID)
	if err != nil {
		r.finish(store.OpFailed, err.Error())
		return err
	}
	var done []*store.Instance
	pending := false
	for _, in := range rollingOrder(insts) {
		if in.Kind != "server" {
			continue
		}
		name := s.instName(in)
		res, err := s.send(ctx, in.HostID, &agentv1.ApplyParams{Port: in.Port, RevisionId: rev.ID, ParentRevisionId: in.AppliedRev, Params: pv.Changed, RestartRequired: pv.Restart})
		if err != nil {
			r.step("参数下发 "+name, in.ID, "failed", err.Error())
			s.rollbackParams(ctx, r, done, pv, rev)
			r.finish(store.OpRolledBack, err.Error())
			return err
		}
		st := "ok"
		if res.Status == agentv1.CommandResult_PENDING_RESTART {
			st, pending = "pending_restart", true
		}
		r.step("参数下发 "+name, in.ID, st, res.Details["pending"])
		s.Store.DB.Model(in).Update("applied_rev", rev.ID)
		in.AppliedRev = rev.ID
		s.saveStatus(in, res.GetInstance())
		done = append(done, in)
	}
	s.Store.DB.Model(&store.Unit{}).Where("id = ?", unitID).Update("revision_id", rev.ID)
	if pending && ch.Restart {
		if err := s.rollingRestart(ctx, r, unitID, ch.Switchover); err != nil {
			r.finish(store.OpPartial, "parameters applied, rolling restart stopped: "+err.Error())
			return err
		}
	}
	r.finish(store.OpApplied, "")
	return nil
}

// rollbackParams 把已经应用新版本的实例改回旧值（新版本 → 旧版本号，parent 为新版本）。
func (s *Service) rollbackParams(ctx context.Context, r *opRun, done []*store.Instance, pv *Preview, rev *store.ParamRevision) {
	for i := len(done) - 1; i >= 0; i-- {
		in := done[i]
		_, err := s.send(ctx, in.HostID, &agentv1.ApplyParams{Port: in.Port, RevisionId: pv.Revision, ParentRevisionId: rev.ID, Params: pv.Old, RestartRequired: pv.Restart})
		if err != nil {
			r.step("回滚 "+s.instName(in), in.ID, "failed", err.Error())
			continue
		}
		s.Store.DB.Model(in).Update("applied_rev", pv.Revision)
		r.step("回滚 "+s.instName(in), in.ID, "rolled_back", "")
	}
}

func (s *Service) instName(in *store.Instance) string {
	h, err := s.host(in.HostID)
	if err != nil {
		return fmt.Sprintf("#%d:%d", in.HostID, in.Port)
	}
	return fmt.Sprintf("%s:%d", h.Name, in.Port)
}

// rollingRestart 逐个重启：从节点在前，每个重启后等复制链路恢复；主节点可先协调切换再重启。
func (s *Service) rollingRestart(ctx context.Context, r *opRun, unitID uint, switchover bool) error {
	insts, err := s.unitInstances(unitID)
	if err != nil {
		return err
	}
	order := rollingOrder(insts)
	for _, in := range order {
		if isMaster(in) && switchover && len(order) > 1 {
			if err := s.failover(ctx, r, unitID); err != nil {
				return err
			}
			// 切换后旧主节点已是从节点，最后重启它
		}
		res, err := s.send(ctx, in.HostID, &agentv1.InstanceAction{Port: in.Port, Action: agentv1.InstanceAction_RESTART})
		if err != nil {
			r.step("重启 "+s.instName(in), in.ID, "failed", err.Error())
			return err
		}
		s.saveStatus(in, res.GetInstance())
		r.step("重启 "+s.instName(in), in.ID, "ok", "")
		if err := s.waitLink(ctx, in); err != nil {
			r.step("等待复制恢复 "+s.instName(in), in.ID, "failed", err.Error())
			return err
		}
	}
	return nil
}

// waitLink 等待从节点的复制链路为 up（主节点直接通过）。
func (s *Service) waitLink(ctx context.Context, in *store.Instance) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		res, err := s.send(ctx, in.HostID, &agentv1.Probe{Port: in.Port})
		if err == nil {
			st := res.GetInstance()
			s.saveStatus(in, st)
			if st.GetRole() == "master" || st.GetMasterLinkStatus() == "up" {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("replication link did not come up in 2 minutes")
}

// Action 对整个部署单元执行启动 / 停止 / 滚动重启。停止时主节点最后停、启动时主节点先启。
func (s *Service) Action(unitID uint, action string, switchover bool, by string) (*store.Operation, error) {
	var a agentv1.InstanceAction_Action
	switch action {
	case "start":
		a = agentv1.InstanceAction_START
	case "stop":
		a = agentv1.InstanceAction_STOP
	case "restart":
		a = agentv1.InstanceAction_RESTART
	default:
		return nil, invalid("unknown action %q", action)
	}
	var run *opRun
	err := s.Store.DB.Transaction(func(tx *gorm.DB) error {
		var e error
		run, e = s.start(tx, unitID, action, map[string]any{"action": action, "switchover": switchover}, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error {
		var err error
		if a == agentv1.InstanceAction_RESTART {
			err = s.rollingRestart(ctx, r, unitID, switchover)
		} else {
			err = s.simpleAction(ctx, r, unitID, a)
		}
		status := "running"
		if a == agentv1.InstanceAction_STOP {
			status = "stopped"
		}
		if err != nil {
			s.Store.DB.Model(&store.Unit{}).Where("id = ?", unitID).Update("status", "degraded")
			r.finish(store.OpFailed, err.Error())
			return err
		}
		s.Store.DB.Model(&store.Unit{}).Where("id = ?", unitID).Update("status", status)
		r.finish(store.OpApplied, "")
		return nil
	})
	return run.op, nil
}

func (s *Service) simpleAction(ctx context.Context, r *opRun, unitID uint, a agentv1.InstanceAction_Action) error {
	insts, err := s.unitInstances(unitID)
	if err != nil {
		return err
	}
	order := rollingOrder(insts)
	if a == agentv1.InstanceAction_START { // 主节点先启动，从节点随后接上
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	verb := map[agentv1.InstanceAction_Action]string{agentv1.InstanceAction_START: "启动", agentv1.InstanceAction_STOP: "停止"}[a]
	for _, in := range order {
		res, err := s.send(ctx, in.HostID, &agentv1.InstanceAction{Port: in.Port, Action: a})
		if err != nil {
			r.step(verb+" "+s.instName(in), in.ID, "failed", err.Error())
			return err
		}
		s.saveStatus(in, res.GetInstance())
		r.step(verb+" "+s.instName(in), in.ID, "ok", "")
	}
	return nil
}

// Failover 主从切换（协调切换：从节点追平后互换角色，不丢数据）。
func (s *Service) Failover(unitID uint, by string) (*store.Operation, error) {
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, err
	}
	if u.Mode != store.ModeReplication {
		return nil, invalid("switchover is only available for replication units (sentinel / cluster in P3)")
	}
	var run *opRun
	err = s.Store.DB.Transaction(func(tx *gorm.DB) error {
		var e error
		run, e = s.start(tx, unitID, "failover", map[string]any{}, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error {
		if err := s.failover(ctx, r, unitID); err != nil {
			r.finish(store.OpFailed, err.Error())
			return err
		}
		r.finish(store.OpApplied, "")
		return nil
	})
	return run.op, nil
}

func (s *Service) failover(ctx context.Context, r *opRun, unitID uint) error {
	insts, err := s.unitInstances(unitID)
	if err != nil {
		return err
	}
	var master *store.Instance
	for _, in := range insts {
		if isMaster(in) {
			master = in
		}
	}
	if master == nil || len(insts) < 2 {
		return errors.New("unit has no master with replicas")
	}
	if _, err := s.send(ctx, master.HostID, &agentv1.Failover{Port: master.Port, Mode: agentv1.Failover_COORDINATED, TimeoutMs: 10000}); err != nil {
		r.step("协调切换 "+s.instName(master), master.ID, "failed", err.Error())
		return err
	}
	// 重新探测全部实例，按实际角色更新期望角色
	newMaster := ""
	for _, in := range insts {
		res, err := s.send(ctx, in.HostID, &agentv1.Probe{Port: in.Port})
		if err != nil {
			continue
		}
		st := res.GetInstance()
		s.saveStatus(in, st)
		in.Role = st.GetRole()
		desired := "replica"
		if st.GetRole() == "master" {
			desired, newMaster = "master", s.instName(in)
		}
		s.Store.DB.Model(in).Update("desired", desired)
		in.Desired = desired
	}
	r.step("协调切换 "+s.instName(master), master.ID, "ok", "新主节点 "+newMaster)
	return nil
}

// DeleteUnit 停止并删除部署单元的全部实例（数据目录在主机上移入回收区保留）。
func (s *Service) DeleteUnit(unitID uint, by string) (*store.Operation, error) {
	var run *opRun
	err := s.Store.DB.Transaction(func(tx *gorm.DB) error {
		var e error
		run, e = s.start(tx, unitID, "delete", map[string]any{}, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.Store.DB.Model(&store.Unit{}).Where("id = ?", unitID).Update("status", "deleting")
	s.background(run, func(ctx context.Context, r *opRun) error {
		insts, _ := s.unitInstances(unitID)
		for _, in := range insts {
			for _, a := range []agentv1.InstanceAction_Action{agentv1.InstanceAction_STOP, agentv1.InstanceAction_DELETE} {
				if _, err := s.send(ctx, in.HostID, &agentv1.InstanceAction{Port: in.Port, Action: a}); err != nil && !strings.Contains(err.Error(), "NOT_FOUND") {
					r.step("删除 "+s.instName(in), in.ID, "failed", err.Error())
					s.Store.DB.Model(&store.Unit{}).Where("id = ?", unitID).Update("status", "failed")
					r.finish(store.OpFailed, err.Error())
					return err
				}
			}
			s.Store.DB.Delete(in)
			r.step("删除 "+s.instName(in), in.ID, "ok", "")
		}
		s.Store.DB.Where("unit_id = ?", unitID).Delete(&store.AclAccount{})
		r.finish(store.OpApplied, "")
		s.Store.DB.Delete(&store.Unit{}, unitID)
		return nil
	})
	return run.op, nil
}

// AccountSpec 是业务账号请求。口令只在请求中出现一次，库中只存 sha256。
type AccountSpec struct {
	Name        string   `json:"name"`
	Password    string   `json:"password,omitempty"` // 为空且是新账号时自动生成
	KeyPatterns []string `json:"key_patterns"`
	Categories  []string `json:"categories"`
	Enabled     bool     `json:"enabled"`
	KeepOld     bool     `json:"keep_old_password"` // 轮换：新旧口令同时有效，直到下次修改
}

// SaveAccount 新建或修改业务账号并同步到单元的全部实例；返回自动生成的口令（只此一次）。
func (s *Service) SaveAccount(ctx context.Context, unitID uint, spec AccountSpec, by string) (string, error) {
	var a store.AclAccount
	err := s.Store.DB.Where("unit_id = ? AND name = ?", unitID, spec.Name).First(&a).Error
	isNew := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !isNew {
		return "", err
	}
	generated := ""
	if spec.Password == "" && isNew {
		spec.Password = secret()[:24]
		generated = spec.Password
	}
	hashes := splitCSV(a.PassHashes)
	if spec.Password != "" {
		if len(spec.Password) < 12 {
			return "", invalid("password must be at least 12 characters")
		}
		h := sha256.Sum256([]byte(spec.Password))
		nh := hex.EncodeToString(h[:])
		if spec.KeepOld && len(hashes) > 0 {
			hashes = []string{nh, hashes[0]}
		} else {
			hashes = []string{nh}
		}
	}
	a.UnitID, a.Name, a.Enabled, a.UpdatedBy, a.UpdatedAt = unitID, spec.Name, spec.Enabled, by, time.Now()
	a.PassHashes = strings.Join(hashes, ",")
	a.KeyPatterns = strings.Join(spec.KeyPatterns, ",")
	a.Categories = strings.Join(spec.Categories, ",")
	if err := instance.ValidateUser(AclUser(a)); err != nil {
		return "", invalid("%v", err)
	}
	if err := s.Store.DB.Save(&a).Error; err != nil {
		return "", err
	}
	return generated, s.SyncAccounts(ctx, unitID)
}

// DeleteAccount 删除业务账号并同步。
func (s *Service) DeleteAccount(ctx context.Context, unitID uint, name string) error {
	if err := s.Store.DB.Where("unit_id = ? AND name = ?", unitID, name).Delete(&store.AclAccount{}).Error; err != nil {
		return err
	}
	return s.SyncAccounts(ctx, unitID)
}

// SyncAccounts 把库中的业务账号全量同步到单元的全部数据实例。
func (s *Service) SyncAccounts(ctx context.Context, unitID uint) error {
	users, err := s.aclUsers(unitID)
	if err != nil {
		return err
	}
	insts, err := s.unitInstances(unitID)
	if err != nil {
		return err
	}
	var errs []string
	for _, in := range insts {
		if in.Kind != "server" {
			continue
		}
		if _, err := s.send(ctx, in.HostID, &agentv1.SyncAcl{Port: in.Port, Users: users}); err != nil {
			errs = append(errs, s.instName(in)+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
