package orch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// fakeCluster 模拟 agent 对集群指令的处理：维护每个槽的归属，并渲染 CLUSTER NODES 文本。
// 只实现再平衡路径用到的指令；failMigrate 用于注入某个源主机的 MIGRATE_SLOT 失败。
type fakeCluster struct {
	mu          sync.Mutex
	hostNode    map[uint]string // hostID -> node id
	hostAddr    map[uint]string // hostID -> advertised address
	hostPort    map[uint]uint32
	owner       []string // 槽 -> 节点 ID（"" 表示未分配）
	failMigrate map[uint]bool
	migrateFrom []uint // 每次 MIGRATE_SLOT 的源主机
	sends       int
}

func (f *fakeCluster) Online(uint) bool { return true }

func (f *fakeCluster) Send(_ context.Context, hostID uint, m *agentv1.ServerMessage, _ time.Duration) (*agentv1.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends++
	op := m.GetCluster()
	if op == nil {
		return nil, fmt.Errorf("unexpected command %T", m.GetBody())
	}
	ok := &agentv1.CommandResult{Status: agentv1.CommandResult_OK}
	switch op.Op {
	case agentv1.ClusterOp_NODES:
		return &agentv1.CommandResult{Status: agentv1.CommandResult_OK, Payload: []byte(f.renderNodes())}, nil
	case agentv1.ClusterOp_SETSLOT_IMPORTING, agentv1.ClusterOp_SETSLOT_MIGRATING:
		return ok, nil
	case agentv1.ClusterOp_MIGRATE_SLOT:
		f.migrateFrom = append(f.migrateFrom, hostID)
		if f.failMigrate[hostID] {
			return &agentv1.CommandResult{Status: agentv1.CommandResult_FAILED, ErrorCode: "INJECTED", Message: "injected failure"}, nil
		}
		return &agentv1.CommandResult{Status: agentv1.CommandResult_OK, Payload: []byte(`{"migrated":0}`)}, nil
	case agentv1.ClusterOp_SETSLOT_NODE:
		f.owner[op.Slot] = op.NodeId
		return ok, nil
	}
	return nil, fmt.Errorf("fake cluster: unsupported op %v", op.Op)
}

func (f *fakeCluster) renderNodes() string {
	ranges := map[string][]string{}
	for s := 0; s < 16384; {
		node := f.owner[s]
		start := s
		for s < 16384 && f.owner[s] == node {
			s++
		}
		if node == "" {
			continue
		}
		if s-1 == start {
			ranges[node] = append(ranges[node], fmt.Sprint(start))
		} else {
			ranges[node] = append(ranges[node], fmt.Sprintf("%d-%d", start, s-1))
		}
	}
	var b strings.Builder
	for hostID, node := range f.hostNode {
		port := f.hostPort[hostID]
		fmt.Fprintf(&b, "%s %s:%d@%d master - 0 0 1 connected %s\n", node, f.hostAddr[hostID], port, port+10000, strings.Join(ranges[node], " "))
	}
	return b.String()
}

func (f *fakeCluster) counts() map[string]uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]uint32{}
	for _, node := range f.owner {
		if node != "" {
			out[node]++
		}
	}
	return out
}

func (f *fakeCluster) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

func (f *fakeCluster) setFailMigrate(host uint, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failMigrate[host] = fail
}

type rebalanceFixture struct {
	st      *store.Store
	svc     *Service
	fake    *fakeCluster
	unit    *store.Unit
	hosts   []uint // 主节点所在主机，按 distribution 顺序
	instIDs []uint // 主节点实例 ID，与 hosts 一一对应
	nodeIDs []string
}

var licensedSnapshot = license.Snapshot{State: license.StateValid, Payload: &license.Payload{Features: []license.Feature{license.FeatureReshard}}}

// newRebalanceFixture 创建一个集群单元：每个主节点占一台主机，初始槽数由 distribution 指定（总和须为 16384）。
func newRebalanceFixture(t *testing.T, distribution []uint32) *rebalanceFixture {
	t.Helper()
	st := storetest.Open(t)
	now := time.Now()
	unit := &store.Unit{Name: "rebal", Mode: store.ModeCluster, Shards: uint32(len(distribution)), Status: "running"}
	if err := st.DB.Create(unit).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeCluster{hostNode: map[uint]string{}, hostAddr: map[uint]string{}, hostPort: map[uint]uint32{}, owner: make([]string, 16384), failMigrate: map[uint]bool{}}
	fx := &rebalanceFixture{st: st, fake: fake, unit: unit}
	var start uint32
	for i, n := range distribution {
		nodeID := fmt.Sprintf("%040d", i+1)
		host := &store.Host{Name: fmt.Sprintf("h%d", i), Hostname: fmt.Sprintf("h%d", i), Address: fmt.Sprintf("10.0.0.%d", i+1), Status: "online"}
		if err := st.DB.Create(host).Error; err != nil {
			t.Fatal(err)
		}
		var ranges []string
		for s := start; s < start+n; s++ {
			fake.owner[s] = nodeID
		}
		if n > 0 {
			ranges = append(ranges, fmt.Sprintf("%d-%d", start, start+n-1))
		}
		inst := &store.Instance{UnitID: unit.ID, HostID: host.ID, Port: 7000, Kind: "server", Shard: uint32(i), Desired: "master", Role: "master",
			State: "running", ClusterNodeID: nodeID, ClusterSlots: strings.Join(ranges, " "), ReportedAt: &now}
		if err := st.DB.Create(inst).Error; err != nil {
			t.Fatal(err)
		}
		fake.hostNode[host.ID] = nodeID
		fake.hostAddr[host.ID] = host.Address
		fake.hostPort[host.ID] = inst.Port
		fx.hosts = append(fx.hosts, host.ID)
		fx.instIDs = append(fx.instIDs, inst.ID)
		fx.nodeIDs = append(fx.nodeIDs, nodeID)
		start += n
	}
	if start != 16384 {
		t.Fatalf("fixture distribution sums to %d", start)
	}
	fx.svc = &Service{Store: st, GW: fake, License: func() license.Snapshot { return licensedSnapshot },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Timeout: time.Second}
	return fx
}

func (fx *rebalanceFixture) operationCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := fx.st.DB.Model(&store.Operation{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// 3 个主节点各约 1/3，外加 1 个空主节点（加入后的典型状态）。
var expandedDistribution = []uint32{5461, 5461, 5462, 0}

func TestRebalancePreviewUsesSavedStateAndSendsNothing(t *testing.T) {
	fx := newRebalanceFixture(t, expandedDistribution)
	plan, err := fx.svc.PreviewRebalance(fx.unit.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Balanced || plan.Basis != "saved_state" {
		t.Fatalf("preview balanced=%t basis=%q", plan.Balanced, plan.Basis)
	}
	if len(plan.Steps) != 3 || plan.Moved != 4096 {
		t.Fatalf("steps=%d moved=%d, want 3 steps moving 4096", len(plan.Steps), plan.Moved)
	}
	for _, st := range plan.Steps {
		if st.TargetInstanceID != fx.instIDs[3] {
			t.Fatalf("step %d targets instance %d, want the new empty master", st.Index, st.TargetInstanceID)
		}
	}
	for _, n := range plan.Nodes {
		if n.After != 4096 || n.Target != 4096 {
			t.Fatalf("node %s after=%d target=%d", n.Name, n.After, n.Target)
		}
	}
	if got := fx.fake.sendCount(); got != 0 {
		t.Fatalf("dry run sent %d commands to agents, want 0", got)
	}
	if got := fx.operationCount(t); got != 0 {
		t.Fatalf("dry run created %d operations", got)
	}
}

func TestRebalancePreviewBalancedClusterNeedsNoAction(t *testing.T) {
	fx := newRebalanceFixture(t, []uint32{4096, 4096, 4096, 4096})
	plan, err := fx.svc.PreviewRebalance(fx.unit.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Balanced || len(plan.Steps) != 0 || plan.Moved != 0 {
		t.Fatalf("balanced preview = %+v", plan)
	}
	if fx.fake.sendCount() != 0 {
		t.Fatal("balanced preview sent commands")
	}
}

func TestRebalancePreviewRejectsNonClusterUnit(t *testing.T) {
	fx := newRebalanceFixture(t, []uint32{8192, 8192})
	if err := fx.st.DB.Model(&store.Unit{}).Where("id = ?", fx.unit.ID).Update("mode", store.ModeSentinel).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.PreviewRebalance(fx.unit.ID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("preview on non-cluster unit: %v, want ErrInvalid", err)
	}
	if _, err := fx.svc.Rebalance(fx.unit.ID, RebalanceSpec{}, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rebalance on non-cluster unit: %v, want ErrInvalid", err)
	}
	if fx.fake.sendCount() != 0 || fx.operationCount(t) != 0 {
		t.Fatal("rejected request reached the agent or created an operation")
	}
}

func TestRebalanceRequiresReshardLicense(t *testing.T) {
	fx := newRebalanceFixture(t, expandedDistribution)
	fx.svc.License = func() license.Snapshot { return license.Snapshot{} }
	if _, err := fx.svc.PreviewRebalance(fx.unit.ID, 0); !errors.Is(err, ErrLicense) {
		t.Fatalf("preview without license: %v, want ErrLicense", err)
	}
	if _, err := fx.svc.Rebalance(fx.unit.ID, RebalanceSpec{}, "alice"); !errors.Is(err, ErrLicense) {
		t.Fatalf("rebalance without license: %v, want ErrLicense", err)
	}
	if fx.fake.sendCount() != 0 || fx.operationCount(t) != 0 {
		t.Fatal("unlicensed request reached the agent or created an operation")
	}
}

func TestRebalanceRejectsUnitWithActiveOperation(t *testing.T) {
	fx := newRebalanceFixture(t, expandedDistribution)
	if err := fx.st.LockUnit(fx.unit.ID, 9999); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Rebalance(fx.unit.ID, RebalanceSpec{}, "alice"); !errors.Is(err, store.ErrUnitBusy) {
		t.Fatalf("rebalance on busy unit: %v, want ErrUnitBusy", err)
	}
	if _, err := fx.svc.PreviewRebalance(fx.unit.ID, 0); !errors.Is(err, store.ErrUnitBusy) {
		t.Fatalf("preview on busy unit: %v, want ErrUnitBusy", err)
	}
	if fx.fake.sendCount() != 0 {
		t.Fatal("busy unit reached the agent")
	}
}

func TestRebalanceExecutesPlanAndBalancesCluster(t *testing.T) {
	fx := newRebalanceFixture(t, expandedDistribution)
	op, err := fx.svc.Rebalance(fx.unit.ID, RebalanceSpec{Batch: 100}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	fx.svc.Wait()
	got, err := store.First[store.Operation](fx.st, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.OpApplied || got.Kind != "rebalance" {
		t.Fatalf("operation status=%s kind=%s error=%q", got.Status, got.Kind, got.Error)
	}
	for i, node := range fx.nodeIDs {
		if c := fx.fake.counts()[node]; c != 4096 {
			t.Fatalf("node %d holds %d slots after rebalance, want 4096", i, c)
		}
	}
	var total uint32
	for _, c := range fx.fake.counts() {
		total += c
	}
	if total != 16384 {
		t.Fatalf("total slots = %d, want 16384", total)
	}
	if !strings.Contains(got.Steps, "再平衡第 3/3 步") {
		t.Fatalf("operation steps do not record the plan progress: %s", got.Steps)
	}
	if got.Error != "" {
		t.Fatalf("applied operation has error %q", got.Error)
	}
}

func TestRebalanceStopsAtFailedStepAndResumes(t *testing.T) {
	fx := newRebalanceFixture(t, expandedDistribution)
	// 步骤按源节点标识排序执行：第 2 步的源是 fx.hosts[1]。
	fx.fake.setFailMigrate(fx.hosts[1], true)
	op, err := fx.svc.Rebalance(fx.unit.ID, RebalanceSpec{Batch: 100}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	fx.svc.Wait()
	got, err := store.First[store.Operation](fx.st, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.OpPaused {
		t.Fatalf("status = %s, want PAUSED", got.Status)
	}
	if !strings.Contains(got.Error, "rebalance step 2/3") {
		t.Fatalf("error does not identify the stopped step: %q", got.Error)
	}
	for _, h := range fx.fake.migrateFrom {
		if h == fx.hosts[2] {
			t.Fatal("step 3 was executed after step 2 failed")
		}
	}
	if !strings.Contains(got.Steps, "失败") {
		t.Fatalf("failed step not recorded: %s", got.Steps)
	}
	// 第 1 步已完成且不回滚：源节点 0 已降到目标值。
	if c := fx.fake.counts()[fx.nodeIDs[0]]; c != 4096 {
		t.Fatalf("completed step was rolled back: node 0 holds %d", c)
	}

	// 故障恢复后，以另一身份恢复操作；计划按实时槽位重算，已完成的步骤不再重复。
	fx.fake.setFailMigrate(fx.hosts[1], false)
	resumed, err := fx.svc.ResumeOperation(op.ID, "bob")
	if err != nil {
		t.Fatal(err)
	}
	fx.svc.Wait()
	final, err := store.First[store.Operation](fx.st, resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.OpApplied {
		t.Fatalf("resumed status = %s error=%q", final.Status, final.Error)
	}
	for i, node := range fx.nodeIDs {
		if c := fx.fake.counts()[node]; c != 4096 {
			t.Fatalf("after resume node %d holds %d, want 4096", i, c)
		}
	}
}
