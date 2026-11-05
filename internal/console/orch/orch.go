// Package orch 是控制台的编排层（CacheOS 规格书 6.3、6.4、7 节 M4–M6、M11）：
// 部署单元的创建、参数变更、启停与滚动重启、主从切换、业务账号同步。
//
// 每个部署单元同时只允许一个变更（数据库行锁 unit_locks）；变更记录为 Operation，逐步骤保存结果，
// 后台执行，API 立即返回变更 ID。所有指令经 gateway 签名下发，agent 侧再校验一次。
package orch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
	"github.com/xiaoguan521/cacheos/internal/pkg/secretbox"
)

// Sender 下发签名指令（gateway.Server 实现）。
type Sender interface {
	Send(ctx context.Context, hostID uint, m *agentv1.ServerMessage, timeout time.Duration) (*agentv1.CommandResult, error)
	Online(hostID uint) bool
}

// Service 是编排服务。
type Service struct {
	Store   *store.Store
	GW      Sender
	Box     *secretbox.Box
	Log     *slog.Logger
	License func() license.Snapshot
	PortMin uint32 // 默认 7000
	PortMax uint32 // 默认 7999
	Timeout time.Duration

	wg sync.WaitGroup
}

// 错误。
var (
	ErrInvalid     = errors.New("invalid request")
	ErrUnsupported = errors.New("not supported yet")
	ErrNoHosts     = errors.New("not enough online hosts")
	ErrNoChanges   = errors.New("no parameter changes")
	ErrLicense     = errors.New("license limit")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Wait 等待后台变更全部结束（测试与优雅退出用）。
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 3 * time.Minute
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

// UnitSpec 是创建部署单元的请求。
type UnitSpec struct {
	Name        string            `json:"name"`
	Mode        string            `json:"mode"`
	Description string            `json:"description"`
	MemoryMB    uint32            `json:"memory_mb"`
	Replicas    uint32            `json:"replicas"`
	TemplateID  uint              `json:"template_id"`
	Params      map[string]string `json:"params"`   // 在模板之上的覆盖
	HostIDs     []uint            `json:"host_ids"` // 可选：指定主机（第一台放主节点）
}

// Step 是变更中的一步。
type Step struct {
	Name       string    `json:"name"`
	InstanceID uint      `json:"instance_id,omitempty"`
	Status     string    `json:"status"` // ok | failed | skipped | pending_restart | rolled_back
	Message    string    `json:"message,omitempty"`
	At         time.Time `json:"at"`
}

type opRun struct {
	s     *Service
	op    *store.Operation
	steps []Step
}

func (r *opRun) step(name string, inst uint, status, msg string) {
	r.steps = append(r.steps, Step{Name: name, InstanceID: inst, Status: status, Message: msg, At: time.Now()})
	b, _ := json.Marshal(r.steps)
	r.s.Store.DB.Model(&store.Operation{}).Where("id = ?", r.op.ID).Update("steps", string(b))
}

func (r *opRun) finish(status, errMsg string) {
	now := time.Now()
	r.s.Store.DB.Model(&store.Operation{}).Where("id = ?", r.op.ID).Updates(map[string]any{"status": status, "error": errMsg, "finished_at": &now})
	_ = r.s.Store.UnlockUnit(r.op.UnitID, r.op.ID)
}

// start 创建变更记录并加单元锁；返回的 opRun 在后台执行。
func (s *Service) start(tx *gorm.DB, unitID uint, kind string, req any, by string) (*opRun, error) {
	b, _ := json.Marshal(req)
	now := time.Now()
	op := &store.Operation{UnitID: unitID, Kind: kind, Status: store.OpApplying, Request: string(b), CreatedBy: by, StartedAt: &now}
	if err := tx.Create(op).Error; err != nil {
		return nil, err
	}
	st := &store.Store{DB: tx}
	if err := st.LockUnit(unitID, op.ID); err != nil {
		return nil, err
	}
	return &opRun{s: s, op: op}, nil
}

func (s *Service) background(r *opRun, f func(ctx context.Context, r *opRun) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx := context.Background()
		defer func() {
			if p := recover(); p != nil {
				r.finish(store.OpFailed, fmt.Sprint("internal error: ", p))
			}
		}()
		if err := f(ctx, r); err != nil {
			s.Log.Warn("operation failed", "op", r.op.ID, "kind", r.op.Kind, "err", err)
		}
	}()
}

func secret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// TemplateParams 返回模板参数（内置模板在首次使用时写入库中）。
func (s *Service) TemplateParams(id uint) (map[string]string, error) {
	if id == 0 {
		return map[string]string{}, nil
	}
	t, err := store.First[store.ParamTemplate](s.Store, id)
	if err != nil {
		return nil, invalid("template %d not found", id)
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(t.Params), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// EnsureBuiltinTemplates 写入三套内置模板（已存在则跳过）。
func (s *Service) EnsureBuiltinTemplates() error {
	for _, t := range paramcat.Builtin {
		var n int64
		s.Store.DB.Model(&store.ParamTemplate{}).Where("name = ?", t.Name).Count(&n)
		if n > 0 {
			continue
		}
		b, _ := json.Marshal(t.Params)
		if err := s.Store.DB.Create(&store.ParamTemplate{Name: t.Name, Description: t.Description, Params: string(b), Builtin: true, UpdatedBy: "system", UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
	}
	return nil
}

// licenseCheck 校验实例数与内存总量上限（不影响已有实例，规格书 10 节）。
func (s *Service) licenseCheck(addInstances int, addMemoryMB uint64) error {
	if s.License == nil {
		return nil
	}
	snap := s.License()
	if !snap.Writable() {
		return fmt.Errorf("%w: license is %s", ErrLicense, snap.State)
	}
	if snap.Payload == nil {
		return nil
	}
	lim := snap.Payload.Limits
	if lim.Instances > 0 {
		var n int64
		s.Store.DB.Model(&store.Instance{}).Count(&n)
		if int(n)+addInstances > lim.Instances {
			return fmt.Errorf("%w: %d instances licensed", ErrLicense, lim.Instances)
		}
	}
	if lim.MemoryGB > 0 {
		var used uint64
		var units []store.Unit
		s.Store.DB.Find(&units)
		for _, u := range units {
			used += uint64(u.MemoryMB) * uint64(u.Shards*(1+u.Replicas))
		}
		if used+addMemoryMB > uint64(lim.MemoryGB)*1024 {
			return fmt.Errorf("%w: %d GB of cache memory licensed", ErrLicense, lim.MemoryGB)
		}
	}
	return nil
}

type hostLoad struct {
	h       store.Host
	allocMB uint64
	ports   map[uint32]bool
}

// place 为一个分片的 n 个数据实例选择主机：只选在线主机，同一分片不放同一台主机，尽量分散到不同 zone，
// 优先选已分配内存少的主机；内存超过物理内存 80% 的主机不选。
func (s *Service) place(n int, memMB uint32, explicit []uint) ([]*hostLoad, error) {
	var hosts []store.Host
	if err := s.Store.DB.Where("status = ?", "online").Find(&hosts).Error; err != nil {
		return nil, err
	}
	var insts []store.Instance
	s.Store.DB.Find(&insts)
	var units []store.Unit
	s.Store.DB.Find(&units)
	unitMem := map[uint]uint32{}
	for _, u := range units {
		unitMem[u.ID] = u.MemoryMB
	}
	loads := map[uint]*hostLoad{}
	var cands []*hostLoad
	for _, h := range hosts {
		if s.GW != nil && !s.GW.Online(h.ID) {
			continue
		}
		l := &hostLoad{h: h, ports: map[uint32]bool{}}
		loads[h.ID] = l
		cands = append(cands, l)
	}
	for _, in := range insts {
		if l := loads[in.HostID]; l != nil {
			l.ports[in.Port] = true
			if in.Kind == "server" {
				l.allocMB += uint64(unitMem[in.UnitID])
			}
		}
	}
	fits := func(l *hostLoad) bool {
		return l.h.MemTotalBytes == 0 || (l.allocMB+uint64(memMB))<<20 <= l.h.MemTotalBytes*8/10
	}
	if len(explicit) > 0 {
		if len(explicit) != n {
			return nil, invalid("host_ids must list %d hosts", n)
		}
		var out []*hostLoad
		seen := map[uint]bool{}
		for _, id := range explicit {
			l := loads[id]
			if l == nil || seen[id] {
				return nil, fmt.Errorf("%w: host %d is offline, unknown or listed twice", ErrNoHosts, id)
			}
			if !fits(l) {
				return nil, fmt.Errorf("%w: host %s lacks memory", ErrNoHosts, l.h.Name)
			}
			seen[id] = true
			out = append(out, l)
		}
		return out, nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].allocMB != cands[j].allocMB {
			return cands[i].allocMB < cands[j].allocMB
		}
		return cands[i].h.ID < cands[j].h.ID
	})
	var out []*hostLoad
	zones := map[string]bool{}
	// 第一轮：每个 zone 一台；第二轮：补足
	for pass := 0; pass < 2 && len(out) < n; pass++ {
		for _, l := range cands {
			if len(out) == n {
				break
			}
			if !fits(l) || contains(out, l) || (pass == 0 && zones[l.h.Zone]) {
				continue
			}
			zones[l.h.Zone] = true
			out = append(out, l)
		}
	}
	if len(out) < n {
		return nil, fmt.Errorf("%w: need %d hosts with free memory on different machines, found %d", ErrNoHosts, n, len(out))
	}
	return out, nil
}

func contains(ls []*hostLoad, l *hostLoad) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}

func (s *Service) freePort(l *hostLoad) (uint32, error) {
	lo, hi := s.PortMin, s.PortMax
	if lo == 0 {
		lo, hi = 7000, 7999
	}
	for p := lo; p <= hi; p++ {
		if !l.ports[p] {
			l.ports[p] = true
			return p, nil
		}
	}
	return 0, fmt.Errorf("%w: no free port on host %s", ErrNoHosts, l.h.Name)
}

// CreateUnit 创建部署单元并在后台创建实例。
func (s *Service) CreateUnit(spec UnitSpec, by string) (*store.Unit, *store.Operation, error) {
	if !nameRe.MatchString(spec.Name) {
		return nil, nil, invalid("name must match %s", nameRe)
	}
	switch spec.Mode {
	case store.ModeStandalone:
		spec.Replicas = 0
	case store.ModeReplication:
		if spec.Replicas == 0 {
			spec.Replicas = 1
		}
		if spec.Replicas > 5 {
			return nil, nil, invalid("at most 5 replicas")
		}
	case store.ModeSentinel, store.ModeCluster:
		return nil, nil, fmt.Errorf("%w: mode %s is delivered in P3", ErrUnsupported, spec.Mode)
	default:
		return nil, nil, invalid("unknown mode %q", spec.Mode)
	}
	if spec.MemoryMB < 64 || spec.MemoryMB > 1<<20 {
		return nil, nil, invalid("memory_mb must be between 64 and 1048576")
	}
	params, err := s.TemplateParams(spec.TemplateID)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range spec.Params {
		if p, ok := paramcat.Lookup(k); ok && p.Name == "maxmemory" {
			return nil, nil, invalid("maxmemory is set from memory_mb")
		}
		params[k] = v
	}
	params["maxmemory"] = strconv.Itoa(int(spec.MemoryMB)) + "mb"
	params, err = paramcat.NormalizeAll(params)
	if err != nil {
		return nil, nil, invalid("%v", err)
	}
	n := 1 + int(spec.Replicas)
	if err := s.licenseCheck(n, uint64(spec.MemoryMB)*uint64(n)); err != nil {
		return nil, nil, err
	}
	hosts, err := s.place(n, spec.MemoryMB, spec.HostIDs)
	if err != nil {
		return nil, nil, err
	}
	sealed, err := s.Box.Seal(secret())
	if err != nil {
		return nil, nil, err
	}
	unit := &store.Unit{Name: spec.Name, Mode: spec.Mode, Description: spec.Description, MemoryMB: spec.MemoryMB, Shards: 1, Replicas: spec.Replicas,
		TemplateID: spec.TemplateID, Status: "creating", MasterAuth: sealed, CreatedBy: by}
	var run *opRun
	var insts []*store.Instance
	err = s.Store.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(unit).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return invalid("unit %s already exists", spec.Name)
			}
			return err
		}
		rev, err := s.newRevision(tx, unit.ID, "", params, "初始参数", by)
		if err != nil {
			return err
		}
		unit.RevisionID = rev.ID
		if err := tx.Model(unit).Update("revision_id", rev.ID).Error; err != nil {
			return err
		}
		for i, l := range hosts {
			port, err := s.freePort(l)
			if err != nil {
				return err
			}
			role := "replica"
			if i == 0 {
				role = "master"
			}
			in := &store.Instance{UnitID: unit.ID, HostID: l.h.ID, Port: port, Kind: "server", Desired: role, State: "creating"}
			if err := tx.Create(in).Error; err != nil {
				return err
			}
			insts = append(insts, in)
		}
		run, err = s.start(tx, unit.ID, "create", spec, by)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error { return s.runCreate(ctx, r, unit, params, insts) })
	return unit, run.op, nil
}

func (s *Service) newRevision(tx *gorm.DB, unitID uint, parent string, params map[string]string, note, by string) (*store.ParamRevision, error) {
	id, err := store.NextRevisionID(tx)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(params)
	rev := &store.ParamRevision{ID: id, UnitID: unitID, ParentID: parent, Params: string(b), Digest: paramDigest(params), Note: note, CreatedBy: by, CreatedAt: time.Now()}
	return rev, tx.Create(rev).Error
}

func paramDigest(m map[string]string) string {
	// 与 agent 上报的 config_digest 不同：这里只覆盖版本中的参数
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func (s *Service) host(id uint) (*store.Host, error) { return store.First[store.Host](s.Store, id) }

func (s *Service) send(ctx context.Context, hostID uint, body any) (*agentv1.CommandResult, error) {
	m := &agentv1.ServerMessage{}
	switch b := body.(type) {
	case *agentv1.CreateInstance:
		m.Body = &agentv1.ServerMessage_Create{Create: b}
	case *agentv1.ApplyParams:
		m.Body = &agentv1.ServerMessage_Params{Params: b}
	case *agentv1.InstanceAction:
		m.Body = &agentv1.ServerMessage_Action{Action: b}
	case *agentv1.Failover:
		m.Body = &agentv1.ServerMessage_Failover{Failover: b}
	case *agentv1.SyncAcl:
		m.Body = &agentv1.ServerMessage_Acl{Acl: b}
	case *agentv1.SetReplication:
		m.Body = &agentv1.ServerMessage_Replication{Replication: b}
	case *agentv1.Probe:
		m.Body = &agentv1.ServerMessage_Probe{Probe: b}
	case *agentv1.SwitchVersion:
		m.Body = &agentv1.ServerMessage_SwitchVersion{SwitchVersion: b}
	default:
		return nil, fmt.Errorf("unknown command %T", body)
	}
	r, err := s.GW.Send(ctx, hostID, m, s.timeout())
	if err != nil {
		return nil, err
	}
	if r.Status == agentv1.CommandResult_FAILED || r.Status == agentv1.CommandResult_ROLLED_BACK {
		code := r.ErrorCode
		if code == "" {
			code = r.Status.String()
		}
		return r, fmt.Errorf("%s: %s", code, r.Message)
	}
	return r, nil
}

// saveStatus 用指令结果中的实例状态立即更新库（不等下一次心跳）。
func (s *Service) saveStatus(inst *store.Instance, st *agentv1.InstanceStatus) {
	if st == nil {
		return
	}
	u := map[string]any{"state": stateName(st.State), "role": st.Role, "master_host": st.MasterHost, "master_port": st.MasterPort,
		"link_status": st.MasterLinkStatus, "used_memory": st.UsedMemory, "max_memory": st.Maxmemory, "error": st.Error,
		"pending_restart": st.State == agentv1.State_STATE_PENDING_RESTART}
	s.Store.DB.Model(&store.Instance{}).Where("id = ?", inst.ID).Updates(u)
}

func stateName(st agentv1.State) string {
	switch st {
	case agentv1.State_STATE_RUNNING:
		return "running"
	case agentv1.State_STATE_STOPPED:
		return "stopped"
	case agentv1.State_STATE_LOADING:
		return "loading"
	case agentv1.State_STATE_FAILED:
		return "failed"
	case agentv1.State_STATE_PENDING_RESTART:
		return "pending_restart"
	}
	return "unknown"
}

func (s *Service) aclUsers(unitID uint) ([]*agentv1.AclUser, error) {
	var accts []store.AclAccount
	if err := s.Store.DB.Where("unit_id = ?", unitID).Order("name").Find(&accts).Error; err != nil {
		return nil, err
	}
	var out []*agentv1.AclUser
	for _, a := range accts {
		out = append(out, AclUser(a))
	}
	return out, nil
}

// AclUser 把库中的业务账号转换为下发格式。
func AclUser(a store.AclAccount) *agentv1.AclUser {
	u := &agentv1.AclUser{Name: a.Name, Enabled: a.Enabled}
	for _, h := range splitCSV(a.PassHashes) {
		u.PasswordSha256 = append(u.PasswordSha256, h)
	}
	for _, p := range splitCSV(a.KeyPatterns) {
		u.Rules = append(u.Rules, "~"+p)
	}
	u.Rules = append(u.Rules, splitCSV(a.Categories)...)
	return u
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Service) runCreate(ctx context.Context, r *opRun, unit *store.Unit, params map[string]string, insts []*store.Instance) error {
	auth, err := s.Box.Open(unit.MasterAuth)
	if err != nil {
		r.finish(store.OpFailed, "open secret: "+err.Error())
		return err
	}
	users, _ := s.aclUsers(unit.ID)
	var master *store.Instance
	var masterHost *store.Host
	failed := ""
	for _, in := range insts {
		h, err := s.host(in.HostID)
		if err != nil {
			failed = err.Error()
			break
		}
		req := &agentv1.CreateInstance{Port: in.Port, Kind: agentv1.Kind_KIND_SERVER, Params: params, RevisionId: unit.RevisionID, Masterauth: auth, AclUsers: users}
		if in.Desired == "replica" && master != nil {
			req.ReplicaOf = &agentv1.ReplicaOf{Host: masterHost.Address, Port: master.Port}
		}
		name := fmt.Sprintf("创建%s %s:%d", roleName(in.Desired), h.Name, in.Port)
		res, err := s.send(ctx, in.HostID, req)
		if err != nil {
			r.step(name, in.ID, "failed", err.Error())
			s.Store.DB.Model(in).Updates(map[string]any{"state": "failed", "error": err.Error()})
			failed = err.Error()
			break
		}
		s.Store.DB.Model(in).Update("applied_rev", unit.RevisionID)
		s.saveStatus(in, res.GetInstance())
		r.step(name, in.ID, "ok", "")
		if in.Desired == "master" {
			master, masterHost = in, h
		}
	}
	if failed != "" {
		s.Store.DB.Model(unit).Update("status", "failed")
		r.finish(store.OpFailed, failed)
		return errors.New(failed)
	}
	s.Store.DB.Model(unit).Update("status", "running")
	r.finish(store.OpApplied, "")
	return nil
}

func roleName(r string) string {
	switch r {
	case "master":
		return "主节点"
	case "replica":
		return "从节点"
	case "sentinel":
		return "哨兵"
	}
	return r
}
