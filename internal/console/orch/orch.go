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
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/console/pki"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
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
	CA      *pki.CA // 用于为启用数据面 TLS 的实例签发短期服务证书
	PortMin uint32  // 默认 7000
	PortMax uint32  // 默认 7999
	Timeout time.Duration
	// RequireApproval enables the optional DRAFT/PRECHECKED/APPROVED gate.
	// It is deliberately explicit so existing installations can roll it out
	// without changing the behavior of already scripted operations.
	RequireApproval bool

	wg        sync.WaitGroup
	pendingMu sync.Mutex
	pending   map[uint]func(context.Context, *opRun) error
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
	Name         string            `json:"name"`
	Mode         string            `json:"mode"`
	Description  string            `json:"description"`
	MemoryMB     uint32            `json:"memory_mb"`
	Shards       uint32            `json:"shards"`
	Replicas     uint32            `json:"replicas"`
	TLS          bool              `json:"tls"`
	Modules      []string          `json:"modules"` // 扩展模块（json / search / bloom），只在创建时选择
	SentinelName string            `json:"sentinel_name"`
	Quorum       uint32            `json:"quorum"`
	TemplateID   uint              `json:"template_id"`
	Params       map[string]string `json:"params"`   // 在模板之上的覆盖
	HostIDs      []uint            `json:"host_ids"` // 可选：指定主机（第一台放主节点）
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

func (s *Service) q() *query.Query { return query.Use(s.Store.DB) }

func (r *opRun) step(name string, inst uint, status, msg string) {
	r.steps = append(r.steps, Step{Name: name, InstanceID: inst, Status: status, Message: msg, At: time.Now()})
	b, _ := json.Marshal(r.steps)
	op := r.s.q().Operation
	_, _ = op.WithContext(context.Background()).Where(op.ID.Eq(r.op.ID)).Update(op.Steps, string(b))
}

func (r *opRun) hasSuccessfulStep(name string, inst uint) bool {
	for _, step := range r.steps {
		if step.Name == name && step.InstanceID == inst && step.Status == "ok" {
			return true
		}
	}
	return false
}

func (r *opRun) hasSuccessfulStepPrefix(prefix string) bool {
	for _, step := range r.steps {
		if strings.HasPrefix(step.Name, prefix) && step.Status == "ok" {
			return true
		}
	}
	return false
}

func (r *opRun) hasStepPrefix(prefix string) bool {
	for _, step := range r.steps {
		if strings.HasPrefix(step.Name, prefix) {
			return true
		}
	}
	return false
}

func (r *opRun) finish(status, errMsg string) {
	now := time.Now()
	op := r.s.q().Operation
	_, _ = op.WithContext(context.Background()).Where(op.ID.Eq(r.op.ID)).UpdateSimple(op.Status.Value(status), op.Error.Value(errMsg), op.FinishedAt.Value(now))
	_ = r.s.Store.UnlockUnit(r.op.UnitID, r.op.ID)
}

// start 创建变更记录并加单元锁；返回的 opRun 在后台执行。
func (s *Service) start(tx *query.Query, unitID uint, kind string, req any, by string) (*opRun, error) {
	b, _ := json.Marshal(req)
	now := time.Now()
	op := &store.Operation{UnitID: unitID, Kind: kind, Status: store.OpPrechecked, Request: string(b), CreatedBy: by, CreatedAt: now}
	if err := tx.Operation.WithContext(context.Background()).Create(op); err != nil {
		return nil, err
	}
	st := &store.Store{DB: tx.UnderlyingDB()}
	if err := st.LockUnit(unitID, op.ID); err != nil {
		return nil, err
	}
	return &opRun{s: s, op: op}, nil
}

func (s *Service) background(r *opRun, f func(ctx context.Context, r *opRun) error) {
	if s.RequireApproval {
		s.pendingMu.Lock()
		if s.pending == nil {
			s.pending = map[uint]func(context.Context, *opRun) error{}
		}
		s.pending[r.op.ID] = f
		s.pendingMu.Unlock()
		return
	}
	s.runBackground(r, f)
}

func (s *Service) runBackground(r *opRun, f func(ctx context.Context, r *opRun) error) {
	now := time.Now()
	op := s.q().Operation
	if r.op.Steps != "" {
		_ = json.Unmarshal([]byte(r.op.Steps), &r.steps)
	}
	_, _ = op.WithContext(context.Background()).Where(op.ID.Eq(r.op.ID), op.Status.In(store.OpPrechecked, store.OpApproved)).UpdateSimple(op.Status.Value(store.OpApplying), op.StartedAt.Value(now))
	r.op.Status = store.OpApplying
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

// ApproveOperation releases an operation waiting in PRECHECKED. The requester
// and approver must be different identities, and the same unit lock remains in
// place until the approved operation finishes.
func (s *Service) ApproveOperation(id uint, by string) (*store.Operation, error) {
	op, err := store.First[store.Operation](s.Store, id)
	if err != nil {
		return nil, err
	}
	if op.Status != store.OpPrechecked {
		return nil, invalid("operation %d is %s, not PRECHECKED", id, op.Status)
	}
	if strings.TrimSpace(by) == "" || by == op.CreatedBy {
		return nil, invalid("the requester and approver must be different")
	}
	s.pendingMu.Lock()
	f := s.pending[id]
	s.pendingMu.Unlock()
	if f == nil {
		f, err = s.rebuildPending(op)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now()
	q := s.q().Operation
	result, err := q.WithContext(context.Background()).Where(q.ID.Eq(id), q.Status.Eq(store.OpPrechecked)).UpdateSimple(q.Status.Value(store.OpApproved), q.ApprovedBy.Value(by), q.StartedAt.Value(now))
	if err != nil {
		return nil, err
	}
	if result.RowsAffected == 0 {
		return nil, invalid("operation %d is no longer PRECHECKED", id)
	}
	s.pendingMu.Lock()
	delete(s.pending, id)
	s.pendingMu.Unlock()
	s.runBackground(&opRun{s: s, op: op}, f)
	op.Status, op.ApprovedBy, op.StartedAt = store.OpApplying, by, &now
	return op, nil
}

// ResumeOperation rehydrates a paused operation from its durable request and
// steps.  The unit lock is reacquired before the worker starts; the runner
// skips already successful steps so a process restart or a timeout resumes at
// the last durable boundary instead of replaying the whole plan blindly.
func (s *Service) ResumeOperation(id uint, by string) (*store.Operation, error) {
	op, err := store.First[store.Operation](s.Store, id)
	if err != nil {
		return nil, err
	}
	if op.Status != store.OpPaused {
		return nil, invalid("operation %d is %s, not PAUSED", id, op.Status)
	}
	if strings.TrimSpace(by) == "" || by == op.CreatedBy {
		return nil, invalid("the requester and approver must be different")
	}
	switch op.Kind {
	case "params", "restart", "reshard", "rebalance", "upgrade", "scale", "sentinel":
	default:
		return nil, fmt.Errorf("%w: paused operation kind %q cannot be resumed", ErrUnsupported, op.Kind)
	}
	f, err := s.rebuildPending(op)
	if err != nil {
		return nil, err
	}
	if owner, lockErr := s.Store.UnitLockOwner(op.UnitID); lockErr != nil {
		return nil, lockErr
	} else if owner == 0 {
		if err := s.Store.LockUnit(op.UnitID, op.ID); err != nil {
			return nil, err
		}
	} else if owner != op.ID {
		return nil, store.ErrUnitBusy
	}
	now := time.Now()
	q := s.q().Operation
	result, err := q.WithContext(context.Background()).Where(q.ID.Eq(id), q.Status.Eq(store.OpPaused)).UpdateSimple(q.Status.Value(store.OpApproved), q.ApprovedBy.Value(by), q.StartedAt.Value(now), q.Error.Value(""))
	if err != nil {
		_ = s.Store.UnlockUnit(op.UnitID, op.ID)
		return nil, err
	}
	if result.RowsAffected == 0 {
		_ = s.Store.UnlockUnit(op.UnitID, op.ID)
		return nil, invalid("operation %d is no longer PAUSED", id)
	}
	s.runBackground(&opRun{s: s, op: op}, f)
	op.Status, op.ApprovedBy, op.StartedAt = store.OpApplying, by, &now
	return op, nil
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
	tq := s.q().ParamTemplate
	for _, t := range paramcat.Builtin {
		n, err := tq.WithContext(context.Background()).Where(tq.Name.Eq(t.Name)).Count()
		if err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		b, _ := json.Marshal(t.Params)
		if err := tq.WithContext(context.Background()).Create(&store.ParamTemplate{Name: t.Name, Description: t.Description, Params: string(b), Builtin: true, UpdatedBy: "system", UpdatedAt: time.Now()}); err != nil {
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
		n, err := s.q().Instance.WithContext(context.Background()).Count()
		if err != nil {
			return err
		}
		if int(n)+addInstances > lim.Instances {
			return fmt.Errorf("%w: %d instances licensed", ErrLicense, lim.Instances)
		}
	}
	if lim.MemoryGB > 0 {
		var used uint64
		units, err := s.q().Unit.WithContext(context.Background()).Find()
		if err != nil {
			return err
		}
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
	h        store.Host
	allocMB  uint64
	ports    map[uint32]bool
	lastPort uint32
}

// place 为一个分片的 n 个数据实例选择主机：只选在线主机，同一分片不放同一台主机，尽量分散到不同 zone，
// 优先选已分配内存少的主机；内存超过物理内存 80% 或持久化目录空间不足的主机不选。
func (s *Service) place(n int, memMB uint32, diskNeed uint64, explicit []uint) ([]*hostLoad, error) {
	hq := s.q().Host
	hosts, err := hq.WithContext(context.Background()).Where(hq.Status.Eq("online")).Find()
	if err != nil {
		return nil, err
	}
	insts, err := s.q().Instance.WithContext(context.Background()).Find()
	if err != nil {
		return nil, err
	}
	units, err := s.q().Unit.WithContext(context.Background()).Find()
	if err != nil {
		return nil, err
	}
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
		l := &hostLoad{h: *h, ports: map[uint32]bool{}}
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
		memoryOK := l.h.MemTotalBytes == 0 || (l.allocMB+uint64(memMB))<<20 <= l.h.MemTotalBytes*8/10
		diskOK := diskNeed == 0 || l.h.DiskFreeBytes >= diskNeed
		return memoryOK && diskOK
	}
	reason := func(l *hostLoad) string {
		memoryOK := l.h.MemTotalBytes == 0 || (l.allocMB+uint64(memMB))<<20 <= l.h.MemTotalBytes*8/10
		if !memoryOK {
			return "lacks memory"
		}
		if diskNeed > 0 && l.h.DiskFreeBytes < diskNeed {
			return fmt.Sprintf("needs %d free bytes for persistence, has %d", diskNeed, l.h.DiskFreeBytes)
		}
		return "does not meet placement constraints"
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
				return nil, fmt.Errorf("%w: host %s %s", ErrNoHosts, l.h.Name, reason(l))
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

// placeCluster applies anti-affinity per shard rather than globally: a
// three-shard cluster can run on three hosts with each host carrying a
// different shard's replica, while a master and its own replica never share a
// host. This matches the commercial plan's failure-domain rule.
func (s *Service) placeCluster(shards, replicas int, memMB uint32, diskNeed uint64, explicit []uint) ([]*hostLoad, error) {
	hq := s.q().Host
	hosts, err := hq.WithContext(context.Background()).Where(hq.Status.Eq("online")).Find()
	if err != nil {
		return nil, err
	}
	insts, err := s.q().Instance.WithContext(context.Background()).Find()
	if err != nil {
		return nil, err
	}
	units, err := s.q().Unit.WithContext(context.Background()).Find()
	if err != nil {
		return nil, err
	}
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
		l := &hostLoad{h: *h, ports: map[uint32]bool{}}
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
		return (l.h.MemTotalBytes == 0 || (l.allocMB+uint64(memMB))<<20 <= l.h.MemTotalBytes*8/10) && (diskNeed == 0 || l.h.DiskFreeBytes >= diskNeed)
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].allocMB != cands[j].allocMB {
			return cands[i].allocMB < cands[j].allocMB
		}
		return cands[i].h.ID < cands[j].h.ID
	})
	total := shards * (1 + replicas)
	if len(explicit) > 0 && len(explicit) != total {
		return nil, invalid("host_ids must list %d hosts", total)
	}
	var out []*hostLoad
	for shard := 0; shard < shards; shard++ {
		if len(explicit) > 0 {
			continue
		}
		used := map[uint]bool{}
		zones := map[string]bool{}
		for pass := 0; pass < 2 && len(used) < 1+replicas; pass++ {
			for _, l := range cands {
				if len(used) == 1+replicas {
					break
				}
				if used[l.h.ID] || !fits(l) || (pass == 0 && zones[l.h.Zone]) {
					continue
				}
				used[l.h.ID] = true
				zones[l.h.Zone] = true
				l.allocMB += uint64(memMB)
				out = append(out, l)
			}
		}
		if len(used) != 1+replicas {
			return nil, fmt.Errorf("%w: shard %d needs %d different hosts", ErrNoHosts, shard, 1+replicas)
		}
	}
	if len(explicit) > 0 {
		out = out[:0]
		for shard := 0; shard < shards; shard++ {
			used := map[uint]bool{}
			for i := 0; i < 1+replicas; i++ {
				l := loads[explicit[shard*(1+replicas)+i]]
				if l == nil || used[l.h.ID] || !fits(l) {
					return nil, fmt.Errorf("%w: explicit host placement violates shard anti-affinity", ErrNoHosts)
				}
				used[l.h.ID] = true
				l.allocMB += uint64(memMB)
				out = append(out, l)
			}
		}
	}
	return out, nil
}

func (s *Service) freePort(l *hostLoad) (uint32, error) {
	lo, hi := s.PortMin, s.PortMax
	if lo == 0 {
		lo, hi = 7000, 7999
	}
	for p := lo; ; p++ {
		if !l.ports[p] {
			l.ports[p] = true
			return p, nil
		}
		if p == hi {
			break
		}
	}
	return 0, fmt.Errorf("%w: no free port on host %s", ErrNoHosts, l.h.Name)
}

// CreateUnit 创建部署单元并在后台创建实例。
func (s *Service) CreateUnit(spec UnitSpec, by string) (*store.Unit, *store.Operation, error) {
	if !nameRe.MatchString(spec.Name) {
		return nil, nil, invalid("name must match %s", nameRe)
	}
	if spec.Shards == 0 {
		spec.Shards = 1
	}
	dataN := 1
	extraN := 0
	switch spec.Mode {
	case store.ModeStandalone:
		spec.Shards = 1
		spec.Replicas = 0
	case store.ModeReplication:
		spec.Shards = 1
		if spec.Replicas == 0 {
			spec.Replicas = 1
		}
		if spec.Replicas > 5 {
			return nil, nil, invalid("at most 5 replicas")
		}
	case store.ModeSentinel:
		spec.Shards = 1
		if spec.Replicas == 0 {
			spec.Replicas = 1
		}
		if spec.Replicas > 5 {
			return nil, nil, invalid("at most 5 replicas")
		}
		extraN = 3 // sentinel quorum is deliberately odd and independently placed
		if spec.SentinelName == "" {
			spec.SentinelName = spec.Name
		}
		if spec.Quorum == 0 {
			spec.Quorum = 2
		}
	case store.ModeCluster:
		if spec.Shards < 3 || spec.Shards > 1024 {
			return nil, nil, invalid("cluster shards must be between 3 and 1024")
		}
		if spec.Replicas == 0 {
			spec.Replicas = 1
		}
		if spec.Replicas > 5 {
			return nil, nil, invalid("at most 5 replicas per shard")
		}
	default:
		return nil, nil, invalid("unknown mode %q", spec.Mode)
	}
	modules, err := NormalizeUnitModules(spec.Mode, spec.Modules)
	if err != nil {
		return nil, nil, err
	}
	dataN = int(spec.Shards) * (1 + int(spec.Replicas))
	n := dataN + extraN
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
	// Instance limits count every managed process, including the three
	// sentinels; memory limits count only data-plane servers.
	if err := s.licenseCheck(n, uint64(spec.MemoryMB)*uint64(dataN)); err != nil {
		return nil, nil, err
	}
	diskNeed := uint64(0)
	if instance.ParamsNeedPersistence(params) {
		maxMemory, err := instance.MaxMemoryBytes(params)
		if err != nil {
			return nil, nil, invalid("%v", err)
		}
		diskNeed = instance.PersistenceRequiredBytes(maxMemory, 0)
	}
	var hosts []*hostLoad
	if spec.Mode == store.ModeCluster {
		hosts, err = s.placeCluster(int(spec.Shards), int(spec.Replicas), spec.MemoryMB, diskNeed, spec.HostIDs)
	} else {
		hosts, err = s.place(n, spec.MemoryMB, diskNeed, spec.HostIDs)
	}
	if err != nil {
		return nil, nil, err
	}
	sealed, err := s.Box.Seal(secret())
	if err != nil {
		return nil, nil, err
	}
	unit := &store.Unit{Name: spec.Name, Mode: spec.Mode, Description: spec.Description, MemoryMB: spec.MemoryMB, Shards: spec.Shards, Replicas: spec.Replicas,
		TemplateID: spec.TemplateID, TLS: spec.TLS, Modules: strings.Join(modules, ","), SentinelName: spec.SentinelName, Quorum: spec.Quorum, Status: "creating", MasterAuth: sealed, CreatedBy: by}
	var run *opRun
	var insts []*store.Instance
	err = s.q().Transaction(func(tx *query.Query) error {
		if err := tx.Unit.WithContext(context.Background()).Create(unit); err != nil {
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
		if _, err := tx.Unit.WithContext(context.Background()).Where(tx.Unit.ID.Eq(unit.ID)).Update(tx.Unit.RevisionID, rev.ID); err != nil {
			return err
		}
		for i, l := range hosts {
			port, err := s.freePort(l)
			if err != nil {
				return err
			}
			kind, role, shard := "server", "replica", uint32(0)
			if spec.Mode == store.ModeSentinel && i >= dataN {
				kind, role = "sentinel", "sentinel"
			} else {
				dataIndex := i
				if spec.Mode == store.ModeSentinel {
					dataIndex = i
				}
				shard = uint32(dataIndex / (1 + int(spec.Replicas)))
				if dataIndex%(1+int(spec.Replicas)) == 0 {
					role = "master"
				}
			}
			in := &store.Instance{UnitID: unit.ID, HostID: l.h.ID, Port: port, Kind: kind, Shard: shard, Desired: role, State: "creating"}
			if err := tx.Instance.WithContext(context.Background()).Create(in); err != nil {
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

func (s *Service) newRevision(tx *query.Query, unitID uint, parent string, params map[string]string, note, by string) (*store.ParamRevision, error) {
	last, err := tx.ParamRevision.WithContext(context.Background()).Order(tx.ParamRevision.Seq.Desc()).Limit(1).Find()
	if err != nil {
		return nil, err
	}
	var next uint
	if len(last) > 0 {
		next = last[0].Seq
	}
	id := fmt.Sprintf("p%08d", next+1)
	b, _ := json.Marshal(params)
	rev := &store.ParamRevision{ID: id, UnitID: unitID, ParentID: parent, Params: string(b), Digest: paramDigest(params), Note: note, CreatedBy: by, CreatedAt: time.Now()}
	return rev, tx.ParamRevision.WithContext(context.Background()).Create(rev)
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
	case *agentv1.ClusterOp:
		m.Body = &agentv1.ServerMessage_Cluster{Cluster: b}
	case *agentv1.SentinelOp:
		m.Body = &agentv1.ServerMessage_Sentinel{Sentinel: b}
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
	iq := s.q().Instance
	_, _ = iq.WithContext(context.Background()).Where(iq.ID.Eq(inst.ID)).Updates(u)
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
	aq := s.q().AclAccount
	accts, err := aq.WithContext(context.Background()).Where(aq.UnitID.Eq(unitID)).Order(aq.Name).Find()
	if err != nil {
		return nil, err
	}
	var out []*agentv1.AclUser
	for _, a := range accts {
		out = append(out, AclUser(*a))
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
	masters := map[uint32]*store.Instance{}
	masterHosts := map[uint32]*store.Host{}
	failed := ""
	for _, in := range insts {
		h, err := s.host(in.HostID)
		if err != nil {
			failed = err.Error()
			break
		}
		agentParams := map[string]string{}
		for k, v := range params {
			agentParams[k] = v
		}
		// The business listener must bind to the host's internal address.  The
		// address is a typed field in CreateInstance and is independently checked
		// again by the agent before writing the data-plane configuration.
		bind := h.Address
		if bind == "" {
			bind = h.Hostname
		}
		kind := agentv1.Kind_KIND_SERVER
		if in.Kind == "sentinel" {
			kind = agentv1.Kind_KIND_SENTINEL
		}
		req := &agentv1.CreateInstance{Port: in.Port, Kind: kind, Params: agentParams, RevisionId: unit.RevisionID, Masterauth: auth, AclUsers: users,
			ClusterEnabled: unit.Mode == store.ModeCluster, BindAddress: bind, Modules: requestModules(unit, kind)}
		if unit.TLS && in.Kind == "server" {
			if s.CA == nil {
				failed = "data-plane TLS is enabled but the console CA is unavailable"
				break
			}
			cert, err := s.CA.ServerCert([]string{h.Address, h.Hostname, "localhost"})
			if err != nil {
				failed = "issue data-plane TLS certificate: " + err.Error()
				break
			}
			keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
			if err != nil {
				failed = "encode data-plane TLS key: " + err.Error()
				break
			}
			req.Tls = &agentv1.TlsMaterial{
				CertPem:        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
				KeyPem:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
				CaPem:          append([]byte(nil), s.CA.PEM...),
				TlsReplication: s.License != nil && s.License().Feature(license.FeatureTLSInternal),
				TlsCluster:     unit.Mode == store.ModeCluster && s.License != nil && s.License().Feature(license.FeatureTLSInternal),
				AuthClients:    false,
			}
		}
		if in.Kind == "sentinel" {
			master := masters[0]
			host := masterHosts[0]
			if master == nil || host == nil {
				failed = "sentinel master is not ready"
				break
			}
			req.SentinelMonitorName = unit.SentinelName
			req.SentinelMasterHost = host.Address
			if req.SentinelMasterHost == "" {
				req.SentinelMasterHost = host.Hostname
			}
			req.SentinelMasterPort = master.Port
			req.SentinelQuorum = unit.Quorum
		} else if in.Desired == "replica" && unit.Mode != store.ModeCluster {
			master := masters[in.Shard]
			host := masterHosts[in.Shard]
			if master != nil && host != nil {
				hostAddr := host.Address
				if hostAddr == "" {
					hostAddr = host.Hostname
				}
				req.ReplicaOf = &agentv1.ReplicaOf{Host: hostAddr, Port: master.Port}
			}
		}
		name := fmt.Sprintf("创建%s %s:%d", roleName(in.Desired), h.Name, in.Port)
		res, err := s.send(ctx, in.HostID, req)
		if err != nil {
			r.step(name, in.ID, "failed", err.Error())
			iq := s.q().Instance
			_, _ = iq.WithContext(ctx).Where(iq.ID.Eq(in.ID)).UpdateSimple(iq.State.Value("failed"), iq.Error.Value(err.Error()))
			failed = err.Error()
			break
		}
		iq := s.q().Instance
		_, _ = iq.WithContext(ctx).Where(iq.ID.Eq(in.ID)).Update(iq.AppliedRev, unit.RevisionID)
		s.saveStatus(in, res.GetInstance())
		r.step(name, in.ID, "ok", "")
		if in.Kind == "server" && in.Desired == "master" {
			masters[in.Shard], masterHosts[in.Shard] = in, h
		}
	}
	if failed != "" {
		uq := s.q().Unit
		_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unit.ID)).Update(uq.Status, "failed")
		r.finish(store.OpFailed, failed)
		return errors.New(failed)
	}
	if unit.Mode == store.ModeCluster {
		if err := s.bootstrapCluster(ctx, r, unit, insts); err != nil {
			uq := s.q().Unit
			_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unit.ID)).Update(uq.Status, "failed")
			r.finish(store.OpFailed, err.Error())
			return err
		}
	}
	uq := s.q().Unit
	_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(unit.ID)).Update(uq.Status, "running")
	r.finish(store.OpApplied, "")
	return nil
}

func clusterAddress(h *store.Host, port uint32) string {
	addr := h.Address
	if addr == "" {
		addr = h.Hostname
	}
	return fmt.Sprintf("%s:%d", addr, port)
}

func clusterNodeIDs(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || len(f[0]) != 40 {
			continue
		}
		addr := strings.Split(f[1], "@")[0]
		out[addr] = f[0]
	}
	return out
}

func (s *Service) bootstrapCluster(ctx context.Context, r *opRun, unit *store.Unit, insts []*store.Instance) error {
	var servers []*store.Instance
	masters := map[uint32]*store.Instance{}
	replicas := map[uint32][]*store.Instance{}
	for _, in := range insts {
		if in.Kind != "server" {
			continue
		}
		servers = append(servers, in)
		if in.Desired == "master" {
			masters[in.Shard] = in
		} else {
			replicas[in.Shard] = append(replicas[in.Shard], in)
		}
	}
	if len(masters) != int(unit.Shards) {
		return fmt.Errorf("cluster needs %d masters, found %d", unit.Shards, len(masters))
	}
	anchor := masters[0]
	if anchor == nil {
		return errors.New("cluster has no anchor master")
	}
	for _, in := range servers {
		if in.ID == anchor.ID {
			continue
		}
		h, err := s.host(in.HostID)
		if err != nil {
			return err
		}
		if _, err := s.send(ctx, anchor.HostID, &agentv1.ClusterOp{Port: anchor.Port, Op: agentv1.ClusterOp_MEET, PeerHost: func() string {
			if h.Address != "" {
				return h.Address
			}
			return h.Hostname
		}(), PeerPort: in.Port}); err != nil {
			return fmt.Errorf("meet %s: %w", s.instName(in), err)
		}
	}
	var nodes map[string]string
	for i := 0; i < 20; i++ {
		res, err := s.send(ctx, anchor.HostID, &agentv1.ClusterOp{Port: anchor.Port, Op: agentv1.ClusterOp_NODES})
		if err == nil {
			nodes = clusterNodeIDs(res.Payload)
			if len(nodes) >= len(servers) {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if len(nodes) < len(servers) {
		return fmt.Errorf("cluster nodes did not converge: found %d of %d", len(nodes), len(servers))
	}
	nodeID := func(in *store.Instance) (string, error) {
		h, err := s.host(in.HostID)
		if err != nil {
			return "", err
		}
		id := nodes[clusterAddress(h, in.Port)]
		if id == "" {
			return "", fmt.Errorf("node id missing for %s", s.instName(in))
		}
		return id, nil
	}
	for shard := uint32(0); shard < unit.Shards; shard++ {
		master := masters[shard]
		start := uint32((uint64(16384) * uint64(shard)) / uint64(unit.Shards))
		end := uint32((uint64(16384)*uint64(shard+1))/uint64(unit.Shards) - 1)
		if _, err := s.send(ctx, master.HostID, &agentv1.ClusterOp{Port: master.Port, Op: agentv1.ClusterOp_ADD_SLOTS, Slots: []*agentv1.SlotRange{{Start: start, End: end}}}); err != nil {
			return fmt.Errorf("assign slots for shard %d: %w", shard, err)
		}
		mid, err := nodeID(master)
		if err != nil {
			return err
		}
		for _, replica := range replicas[shard] {
			if _, err := s.send(ctx, replica.HostID, &agentv1.ClusterOp{Port: replica.Port, Op: agentv1.ClusterOp_REPLICATE, NodeId: mid}); err != nil {
				return fmt.Errorf("replicate shard %d: %w", shard, err)
			}
		}
	}
	r.step("集群拓扑初始化", 0, "ok", fmt.Sprintf("%d shards, %d servers", unit.Shards, len(servers)))
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
