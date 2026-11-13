package orch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// ScaleSpec 是扩缩容请求。Replicas 表示每个分片的从节点数；Shards 只对
// cluster 生效。AddedInstanceIDs / RemovedInstanceIDs 是服务端在预检时
// 固化的执行计划，避免进程重启后按变化的实例列表重新猜测目标。
type ScaleSpec struct {
	Replicas           *uint32 `json:"replicas,omitempty"`
	Shards             *uint32 `json:"shards,omitempty"`
	AddedInstanceIDs   []uint  `json:"added_instance_ids,omitempty"`
	RemovedInstanceIDs []uint  `json:"removed_instance_ids,omitempty"`
}

type scaleInstancePlan struct {
	Shard   uint32
	Desired string
}

// ScaleUnit 先完成目标拓扑、授权和主机容量预检，再把实例变化与操作记录
// 放在同一个事务中。实际启动、入集群、迁槽和删除都由后台操作执行。
func (s *Service) ScaleUnit(unitID uint, req ScaleSpec, by string) (*store.Operation, error) {
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		return nil, err
	}
	if u.Mode == store.ModeStandalone {
		return nil, invalid("standalone units cannot be scaled")
	}
	if s.License != nil {
		snapshot := s.License()
		if u.Mode == store.ModeSentinel && !snapshot.Feature(license.FeatureSentinel) {
			return nil, fmt.Errorf("%w: sentinel scaling is not licensed", ErrLicense)
		}
		if u.Mode == store.ModeCluster && !snapshot.Feature(license.FeatureCluster) {
			return nil, fmt.Errorf("%w: cluster scaling is not licensed", ErrLicense)
		}
	}
	currentShards, currentReplicas := u.Shards, u.Replicas
	targetShards, targetReplicas := currentShards, currentReplicas
	if req.Shards != nil {
		targetShards = *req.Shards
	}
	if req.Replicas != nil {
		targetReplicas = *req.Replicas
	}
	if u.Mode != store.ModeCluster {
		if req.Shards != nil && targetShards != 1 {
			return nil, invalid("%s units always have one shard", u.Mode)
		}
		targetShards = 1
	}
	if targetReplicas < 1 || targetReplicas > 5 {
		return nil, invalid("replicas must be between 1 and 5")
	}
	if u.Mode == store.ModeCluster && (targetShards < 3 || targetShards > 1024) {
		return nil, invalid("cluster shards must be between 3 and 1024")
	}
	if targetShards == currentShards && targetReplicas == currentReplicas {
		return nil, ErrNoChanges
	}
	if u.Mode == store.ModeCluster && targetShards != currentShards && s.License != nil && !s.License().Feature(license.FeatureReshard) {
		return nil, fmt.Errorf("%w: cluster shard scaling requires online reshard license", ErrLicense)
	}

	insts, err := s.unitInstances(unitID)
	if err != nil {
		return nil, err
	}
	adds, removes := buildScalePlan(u, insts, targetShards, targetReplicas)
	addCount := len(adds)
	if err := s.licenseCheck(addCount, uint64(addCount)*uint64(u.MemoryMB)); err != nil {
		return nil, err
	}
	params, err := s.revParams(u.RevisionID)
	if err != nil {
		return nil, err
	}
	diskNeed := uint64(0)
	if instance.ParamsNeedPersistence(params) {
		maxMemory, err := instance.MaxMemoryBytes(params)
		if err != nil {
			return nil, invalid("%v", err)
		}
		diskNeed = instance.PersistenceRequiredBytes(maxMemory, 0)
	}
	added, err := s.allocateScaleInstances(u, insts, adds, diskNeed)
	if err != nil {
		return nil, err
	}
	removedIDs := make([]uint, 0, len(removes))
	for _, in := range removes {
		removedIDs = append(removedIDs, in.ID)
	}

	req.Replicas = &targetReplicas
	if u.Mode == store.ModeCluster {
		req.Shards = &targetShards
	} else {
		req.Shards = nil
	}
	req.AddedInstanceIDs = nil
	req.RemovedInstanceIDs = removedIDs
	var run *opRun
	err = s.q().Transaction(func(tx *query.Query) error {
		for _, in := range added {
			if err := tx.Instance.WithContext(context.Background()).Create(in); err != nil {
				return err
			}
			req.AddedInstanceIDs = append(req.AddedInstanceIDs, in.ID)
		}
		var e error
		run, e = s.start(tx, unitID, "scale", req, by)
		return e
	})
	if err != nil {
		return nil, err
	}
	s.background(run, func(ctx context.Context, r *opRun) error {
		return s.runScale(ctx, r, unitID, req)
	})
	return run.op, nil
}

func buildScalePlan(u *store.Unit, insts []*store.Instance, targetShards, targetReplicas uint32) ([]scaleInstancePlan, []*store.Instance) {
	byShard := map[uint32][]*store.Instance{}
	for _, in := range insts {
		if in.Kind == "server" {
			byShard[in.Shard] = append(byShard[in.Shard], in)
		}
	}
	var adds []scaleInstancePlan
	var removes []*store.Instance
	// A cluster shard is created master-first so non-cluster replicas can also
	// be created with their master endpoint already present in the plan.
	for shard := u.Shards; shard < targetShards; shard++ {
		adds = append(adds, scaleInstancePlan{Shard: shard, Desired: "master"})
		for i := uint32(0); i < targetReplicas; i++ {
			adds = append(adds, scaleInstancePlan{Shard: shard, Desired: "replica"})
		}
	}
	limitShards := u.Shards
	if targetShards < limitShards {
		limitShards = targetShards
	}
	for shard := uint32(0); shard < limitShards; shard++ {
		currentReplicas := uint32(0)
		for _, in := range byShard[shard] {
			if in.Desired == "replica" {
				currentReplicas++
			}
		}
		for i := currentReplicas; i < targetReplicas; i++ {
			adds = append(adds, scaleInstancePlan{Shard: shard, Desired: "replica"})
		}
		if currentReplicas > targetReplicas {
			replicas := make([]*store.Instance, 0, currentReplicas)
			for _, in := range byShard[shard] {
				if in.Desired == "replica" {
					replicas = append(replicas, in)
				}
			}
			sort.Slice(replicas, func(i, j int) bool { return replicas[i].ID > replicas[j].ID })
			removes = append(removes, replicas[:int(currentReplicas-targetReplicas)]...)
		}
	}
	// For a shard contraction every data process in the removed shard goes
	// away. Sentinel processes are intentionally never part of this list.
	for shard := targetShards; shard < u.Shards; shard++ {
		for _, in := range byShard[shard] {
			removes = append(removes, in)
		}
	}
	return adds, removes
}

func (s *Service) allocateScaleInstances(u *store.Unit, insts []*store.Instance, plans []scaleInstancePlan, diskNeed uint64) ([]*store.Instance, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	loads, err := s.scaleHostLoads(u.MemoryMB, diskNeed)
	if err != nil {
		return nil, err
	}
	usedByShard := map[uint32]map[uint]bool{}
	zonesByShard := map[uint32]map[string]bool{}
	for _, in := range insts {
		if in.Kind != "server" {
			continue
		}
		if usedByShard[in.Shard] == nil {
			usedByShard[in.Shard] = map[uint]bool{}
			zonesByShard[in.Shard] = map[string]bool{}
		}
		usedByShard[in.Shard][in.HostID] = true
		if l := loads[in.HostID]; l != nil && l.h.Zone != "" {
			zonesByShard[in.Shard][l.h.Zone] = true
		}
	}
	var out []*store.Instance
	for _, plan := range plans {
		if usedByShard[plan.Shard] == nil {
			usedByShard[plan.Shard] = map[uint]bool{}
			zonesByShard[plan.Shard] = map[string]bool{}
		}
		l, err := s.pickScaleHostFor(loads, usedByShard[plan.Shard], zonesByShard[plan.Shard], u.MemoryMB, diskNeed, UnitModules(u))
		if err != nil {
			return nil, fmt.Errorf("place shard %d %s: %w", plan.Shard, plan.Desired, err)
		}
		usedByShard[plan.Shard][l.h.ID] = true
		if l.h.Zone != "" {
			zonesByShard[plan.Shard][l.h.Zone] = true
		}
		out = append(out, &store.Instance{UnitID: u.ID, HostID: l.h.ID, Port: l.portsLast(), Kind: "server", Shard: plan.Shard, Desired: plan.Desired, State: "creating"})
	}
	return out, nil
}

// portsLast returns the port allocated by pickScaleHost. Keeping the value on
// the load avoids a second scan that could select a different port.
func (l *hostLoad) portsLast() uint32 { return l.lastPort }

func (s *Service) scaleHostLoads(memMB uint32, diskNeed uint64) (map[uint]*hostLoad, error) {
	hosts, err := s.q().Host.WithContext(context.Background()).Where(s.q().Host.Status.Eq("online")).Find()
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
	for _, h := range hosts {
		if s.GW != nil && !s.GW.Online(h.ID) {
			continue
		}
		loads[h.ID] = &hostLoad{h: *h, ports: map[uint32]bool{}}
	}
	for _, in := range insts {
		if l := loads[in.HostID]; l != nil {
			l.ports[in.Port] = true
			if in.Kind == "server" {
				l.allocMB += uint64(unitMem[in.UnitID])
			}
		}
	}
	return loads, nil
}

func (s *Service) pickScaleHost(loads map[uint]*hostLoad, excluded map[uint]bool, usedZones map[string]bool, memMB uint32, diskNeed uint64) (*hostLoad, error) {
	return s.pickScaleHostFor(loads, excluded, usedZones, memMB, diskNeed, nil)
}

// pickScaleHostFor 同 pickScaleHost，另外只选已安装 need 中全部模块的主机。
func (s *Service) pickScaleHostFor(loads map[uint]*hostLoad, excluded map[uint]bool, usedZones map[string]bool, memMB uint32, diskNeed uint64, need []string) (*hostLoad, error) {
	var candidates []*hostLoad
	for _, l := range loads {
		if excluded[l.h.ID] {
			continue
		}
		if l.h.MemTotalBytes != 0 && (l.allocMB+uint64(memMB))<<20 > l.h.MemTotalBytes*8/10 {
			continue
		}
		if diskNeed > 0 && l.h.DiskFreeBytes < diskNeed {
			continue
		}
		if hostMissingModule(l.h, need) != "" {
			continue
		}
		candidates = append(candidates, l)
	}
	if len(candidates) == 0 {
		if len(need) > 0 {
			return nil, fmt.Errorf("%w: no online host satisfies anti-affinity, capacity and the installed modules %v", ErrNoHosts, need)
		}
		return nil, fmt.Errorf("%w: no online host satisfies anti-affinity and capacity", ErrNoHosts)
	}
	sort.Slice(candidates, func(i, j int) bool {
		iUsed, jUsed := usedZones[candidates[i].h.Zone], usedZones[candidates[j].h.Zone]
		if iUsed != jUsed {
			return !iUsed
		}
		if candidates[i].allocMB != candidates[j].allocMB {
			return candidates[i].allocMB < candidates[j].allocMB
		}
		return candidates[i].h.ID < candidates[j].h.ID
	})
	l := candidates[0]
	l.allocMB += uint64(memMB)
	port, err := s.freeScalePort(l)
	if err != nil {
		return nil, err
	}
	l.lastPort = port
	return l, nil
}

func (s *Service) freeScalePort(l *hostLoad) (uint32, error) {
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

func (s *Service) runScale(ctx context.Context, r *opRun, unitID uint, req ScaleSpec) error {
	setStatus := func(status string) {
		_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).Update(s.q().Unit.Status, status)
	}
	setStatus("changing")
	u, err := store.First[store.Unit](s.Store, unitID)
	if err != nil {
		r.finish(store.OpFailed, err.Error())
		return err
	}
	insts, err := s.unitInstances(unitID)
	if err != nil {
		r.finish(store.OpPaused, err.Error())
		return err
	}
	params, err := s.revParams(u.RevisionID)
	if err != nil {
		setStatus("degraded")
		r.finish(store.OpPaused, err.Error())
		return err
	}
	if s.Box == nil {
		err = errors.New("secretbox unavailable")
		setStatus("degraded")
		r.finish(store.OpPaused, err.Error())
		return err
	}
	auth, err := s.Box.Open(u.MasterAuth)
	if err != nil {
		setStatus("degraded")
		r.finish(store.OpPaused, err.Error())
		return err
	}
	users, err := s.aclUsers(unitID)
	if err != nil {
		setStatus("degraded")
		r.finish(store.OpPaused, err.Error())
		return err
	}
	masters, masterHosts := scaleMasters(insts)
	added := make([]*store.Instance, 0, len(req.AddedInstanceIDs))
	for _, id := range req.AddedInstanceIDs {
		in, findErr := store.First[store.Instance](s.Store, id)
		if findErr != nil {
			err = findErr
			break
		}
		added = append(added, in)
	}
	if err != nil {
		setStatus("degraded")
		r.finish(store.OpPaused, err.Error())
		return err
	}
	sort.SliceStable(added, func(i, j int) bool {
		if added[i].Desired != added[j].Desired {
			return added[i].Desired == "master"
		}
		return added[i].ID < added[j].ID
	})
	for _, in := range added {
		step := "扩容创建实例 " + s.instName(in)
		if r.hasSuccessfulStep(step, in.ID) {
			if in.Desired == "master" {
				h, e := s.host(in.HostID)
				if e != nil {
					return s.scalePause(ctx, r, e)
				}
				masters[in.Shard], masterHosts[in.Shard] = in, h
			}
			continue
		}
		scaleHost, createErr := s.host(in.HostID)
		var reqCreate *agentv1.CreateInstance
		if createErr == nil {
			reqCreate, createErr = s.createRequest(u, in, scaleHost, createInputs{Params: params, MasterAuth: auth, Users: users, Masters: masters, MasterHosts: masterHosts})
		}
		if createErr != nil {
			r.step(step, in.ID, "failed", createErr.Error())
			setStatus("degraded")
			r.finish(store.OpPaused, createErr.Error())
			return createErr
		}
		res, sendErr := s.send(ctx, in.HostID, reqCreate)
		if sendErr != nil {
			// A process may have been created just before the console crashed. A
			// successful probe makes the create boundary idempotent on resume.
			if probe, probeErr := s.send(ctx, in.HostID, &agentv1.Probe{Port: in.Port}); probeErr == nil {
				res = probe
			} else {
				r.step(step, in.ID, "failed", sendErr.Error())
				setStatus("degraded")
				r.finish(store.OpPaused, sendErr.Error())
				return sendErr
			}
		}
		_, _ = s.q().Instance.WithContext(ctx).Where(s.q().Instance.ID.Eq(in.ID)).Update(s.q().Instance.AppliedRev, u.RevisionID)
		s.saveStatus(in, res.GetInstance())
		r.step(step, in.ID, "ok", "")
		if in.Desired == "master" {
			h, hostErr := s.host(in.HostID)
			if hostErr != nil {
				return s.scalePause(ctx, r, hostErr)
			}
			masters[in.Shard], masterHosts[in.Shard] = in, h
		}
	}

	if u.Mode == store.ModeCluster {
		all, e := s.unitInstances(unitID)
		if e != nil {
			return s.scalePause(ctx, r, e)
		}
		anchor := masters[0]
		if anchor == nil {
			return s.scalePause(ctx, r, errors.New("cluster has no shard 0 master"))
		}
		nodes, e := s.ensureScaleClusterMembership(ctx, r, anchor, all, added)
		if e != nil {
			return s.scalePause(ctx, r, e)
		}
		oldShards := u.Shards
		targetShards := oldShards
		if req.Shards != nil {
			targetShards = *req.Shards
		}
		if targetShards > oldShards {
			if e = s.expandClusterScale(ctx, r, u, all, added, nodes, targetShards); e != nil {
				return s.scalePause(ctx, r, e)
			}
		} else if targetShards < oldShards {
			if e = s.shrinkClusterScale(ctx, r, u, all, nodes, targetShards); e != nil {
				return s.scalePause(ctx, r, e)
			}
		}
		if e = s.configureScaleClusterReplicas(ctx, r, u, all, added, nodes); e != nil {
			return s.scalePause(ctx, r, e)
		}
		if e = s.forgetScaleClusterNodes(ctx, r, anchor, all, req.RemovedInstanceIDs, nodes); e != nil {
			return s.scalePause(ctx, r, e)
		}
	}
	if err := s.removeScaledInstances(ctx, r, unitID, req.RemovedInstanceIDs); err != nil {
		return s.scalePause(ctx, r, err)
	}
	targetReplicas := u.Replicas
	if req.Replicas != nil {
		targetReplicas = *req.Replicas
	}
	targetShards := u.Shards
	if req.Shards != nil {
		targetShards = *req.Shards
	}
	if _, err := s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(unitID)).UpdateSimple(s.q().Unit.Replicas.Value(targetReplicas), s.q().Unit.Shards.Value(targetShards), s.q().Unit.Status.Value("running")); err != nil {
		return s.scalePause(ctx, r, err)
	}
	r.finish(store.OpApplied, "")
	return nil
}

func (s *Service) scalePause(ctx context.Context, r *opRun, err error) error {
	_, _ = s.q().Unit.WithContext(ctx).Where(s.q().Unit.ID.Eq(r.op.UnitID)).Update(s.q().Unit.Status, "degraded")
	r.finish(store.OpPaused, err.Error())
	return err
}

func scaleMasters(insts []*store.Instance) (map[uint32]*store.Instance, map[uint32]*store.Host) {
	masters := map[uint32]*store.Instance{}
	hosts := map[uint32]*store.Host{}
	for _, in := range insts {
		if in.Kind == "server" && isMaster(in) {
			masters[in.Shard] = in
		}
	}
	return masters, hosts
}

func (s *Service) ensureScaleClusterMembership(ctx context.Context, r *opRun, anchor *store.Instance, all, added []*store.Instance) (map[string]string, error) {
	anchorHost, err := s.host(anchor.HostID)
	if err != nil {
		return nil, err
	}
	res, err := s.send(ctx, anchor.HostID, &agentv1.ClusterOp{Port: anchor.Port, Op: agentv1.ClusterOp_NODES})
	if err != nil {
		return nil, err
	}
	nodes := clusterNodeIDs(res.Payload)
	for _, in := range added {
		if in.Kind != "server" {
			continue
		}
		h, err := s.host(in.HostID)
		if err != nil {
			return nil, err
		}
		addr := clusterAddress(h, in.Port)
		if nodes[addr] != "" {
			r.step("集群加入 "+s.instName(in), in.ID, "skipped", "node already known")
			continue
		}
		if _, err := s.send(ctx, anchor.HostID, &agentv1.ClusterOp{Port: anchor.Port, Op: agentv1.ClusterOp_MEET, PeerHost: hostAddress(h), PeerPort: in.Port}); err != nil {
			return nil, fmt.Errorf("meet %s: %w", s.instName(in), err)
		}
		r.step("集群加入 "+s.instName(in), in.ID, "ok", "")
	}
	for i := 0; i < 40; i++ {
		res, err = s.send(ctx, anchor.HostID, &agentv1.ClusterOp{Port: anchor.Port, Op: agentv1.ClusterOp_NODES})
		if err == nil {
			nodes = clusterNodeIDs(res.Payload)
			ready := true
			for _, in := range added {
				h, hErr := s.host(in.HostID)
				if hErr != nil || nodes[clusterAddress(h, in.Port)] == "" {
					ready = false
					break
				}
			}
			if ready {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	for _, in := range all {
		if in.Kind != "server" {
			continue
		}
		h, err := s.host(in.HostID)
		if err != nil {
			return nil, err
		}
		if id := nodes[clusterAddress(h, in.Port)]; id != "" {
			in.ClusterNodeID = id
			_, _ = s.q().Instance.WithContext(ctx).Where(s.q().Instance.ID.Eq(in.ID)).Update(s.q().Instance.ClusterNodeID, id)
		}
	}
	if len(added) > 0 {
		for _, in := range added {
			if in.Kind == "server" && nodes[clusterAddress(anchorHost, anchor.Port)] == "" {
				return nil, errors.New("cluster anchor node is not present after membership convergence")
			}
		}
	}
	return nodes, nil
}

func (s *Service) configureScaleClusterReplicas(ctx context.Context, r *opRun, u *store.Unit, all, added []*store.Instance, nodes map[string]string) error {
	masters, _ := scaleMasters(all)
	for _, in := range added {
		if in.Kind != "server" || in.Desired != "replica" {
			continue
		}
		step := "集群复制 " + s.instName(in)
		if r.hasSuccessfulStep(step, in.ID) {
			continue
		}
		master := masters[in.Shard]
		if master == nil {
			return fmt.Errorf("master for shard %d is not available", in.Shard)
		}
		if master.ClusterNodeID == "" {
			h, err := s.host(master.HostID)
			if err != nil {
				return err
			}
			master.ClusterNodeID = nodes[clusterAddress(h, master.Port)]
		}
		if master.ClusterNodeID == "" {
			return fmt.Errorf("cluster node id missing for master %s", s.instName(master))
		}
		if _, err := s.send(ctx, in.HostID, &agentv1.ClusterOp{Port: in.Port, Op: agentv1.ClusterOp_REPLICATE, NodeId: master.ClusterNodeID}); err != nil {
			return fmt.Errorf("replicate %s: %w", s.instName(in), err)
		}
		r.step(step, in.ID, "ok", "")
	}
	return nil
}

func (s *Service) expandClusterScale(ctx context.Context, r *opRun, u *store.Unit, all, added []*store.Instance, nodes map[string]string, targetShards uint32) error {
	var masters []*store.Instance
	var newMasters []*store.Instance
	for _, in := range all {
		if in.Kind != "server" || in.Desired != "master" {
			continue
		}
		if in.Shard < u.Shards {
			masters = append(masters, in)
		}
		if in.Shard >= u.Shards {
			newMasters = append(newMasters, in)
		}
	}
	sort.Slice(masters, func(i, j int) bool { return masters[i].Shard < masters[j].Shard })
	sort.Slice(newMasters, func(i, j int) bool { return newMasters[i].Shard < newMasters[j].Shard })
	if len(newMasters) == 0 {
		return nil
	}
	wantEach := uint32(16384 / targetShards)
	if wantEach == 0 {
		wantEach = 1
	}
	for _, target := range newMasters {
		targetID, err := s.clusterInstanceNodeID(ctx, target, nodes)
		if err != nil {
			return err
		}
		for {
			raw, err := s.clusterNodes(ctx, masters[0])
			if err != nil {
				return err
			}
			current := clusterNodeSlotCount(raw, targetID)
			if current >= wantEach {
				break
			}
			remaining := wantEach - current
			moved := false
			for _, source := range masters {
				sourceID, err := s.clusterInstanceNodeID(ctx, source, clusterNodeIDs(raw))
				if err != nil {
					return err
				}
				if sourceID == targetID {
					continue
				}
				// Keep old masters near the target share while filling the new
				// master's share. This avoids the common 3->4 expansion case
				// leaving one old master almost empty and two still overloaded.
				currentSource := clusterNodeSlotCount(raw, sourceID)
				if currentSource <= wantEach {
					continue
				}
				take := currentSource - wantEach
				if take > remaining {
					take = remaining
				}
				ranges := clusterOwnedRanges(raw, sourceID, take)
				for _, rg := range ranges {
					if err := s.runReshard(ctx, r, source, target, ReshardSpec{SourceInstanceID: source.ID, TargetInstanceID: target.ID, Slots: []*agentv1.SlotRange{rg}, Batch: 100}); err != nil {
						return err
					}
					moved = true
					remaining -= rg.End - rg.Start + 1
					if remaining == 0 {
						break
					}
				}
				if remaining == 0 {
					break
				}
			}
			if !moved {
				return fmt.Errorf("cannot find source slots for new shard %d", target.Shard)
			}
		}
		r.step("扩展分片槽位 "+s.instName(target), target.ID, "ok", fmt.Sprintf("target_slots=%d", wantEach))
	}
	return nil
}

func (s *Service) shrinkClusterScale(ctx context.Context, r *opRun, u *store.Unit, all []*store.Instance, nodes map[string]string, targetShards uint32) error {
	var keep, remove []*store.Instance
	for _, in := range all {
		if in.Kind != "server" || in.Desired != "master" {
			continue
		}
		if in.Shard < targetShards {
			keep = append(keep, in)
		} else {
			remove = append(remove, in)
		}
	}
	if len(keep) == 0 {
		return errors.New("cluster shrink would leave no master")
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].Shard < keep[j].Shard })
	sort.Slice(remove, func(i, j int) bool { return remove[i].Shard < remove[j].Shard })
	for i, source := range remove {
		sourceID, err := s.clusterInstanceNodeID(ctx, source, nodes)
		if err != nil {
			return err
		}
		for {
			raw, err := s.clusterNodes(ctx, keep[0])
			if err != nil {
				return err
			}
			ranges := clusterOwnedRanges(raw, sourceID, 16384)
			if len(ranges) == 0 {
				break
			}
			target := keep[i%len(keep)]
			if err := s.runReshard(ctx, r, source, target, ReshardSpec{SourceInstanceID: source.ID, TargetInstanceID: target.ID, Slots: ranges, Batch: 100}); err != nil {
				return err
			}
		}
		r.step("收缩分片槽位 "+s.instName(source), source.ID, "ok", "slots drained")
	}
	return nil
}

func (s *Service) clusterNodes(ctx context.Context, in *store.Instance) ([]byte, error) {
	res, err := s.send(ctx, in.HostID, &agentv1.ClusterOp{Port: in.Port, Op: agentv1.ClusterOp_NODES})
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (s *Service) clusterInstanceNodeID(ctx context.Context, in *store.Instance, nodes map[string]string) (string, error) {
	if in.ClusterNodeID != "" {
		return in.ClusterNodeID, nil
	}
	h, err := s.host(in.HostID)
	if err != nil {
		return "", err
	}
	if id := nodes[clusterAddress(h, in.Port)]; id != "" {
		in.ClusterNodeID = id
		_, _ = s.q().Instance.WithContext(ctx).Where(s.q().Instance.ID.Eq(in.ID)).Update(s.q().Instance.ClusterNodeID, id)
		return id, nil
	}
	return "", fmt.Errorf("cluster node id missing for %s", s.instName(in))
}

func (s *Service) forgetScaleClusterNodes(ctx context.Context, r *opRun, anchor *store.Instance, all []*store.Instance, ids []uint, nodes map[string]string) error {
	if len(ids) == 0 {
		return nil
	}
	remove := map[uint]bool{}
	for _, id := range ids {
		remove[id] = true
	}
	for _, in := range all {
		if !remove[in.ID] || in.Kind != "server" {
			continue
		}
		step := "集群移除节点 " + s.instName(in)
		if r.hasSuccessfulStep(step, in.ID) {
			continue
		}
		id, err := s.clusterInstanceNodeID(ctx, in, nodes)
		if err != nil {
			// A partially completed FORGET is safe to resume.
			if strings.Contains(err.Error(), "missing") {
				r.step(step, in.ID, "skipped", err.Error())
				continue
			}
			return err
		}
		if _, err := s.send(ctx, anchor.HostID, &agentv1.ClusterOp{Port: anchor.Port, Op: agentv1.ClusterOp_FORGET, NodeId: id}); err != nil && !strings.Contains(strings.ToLower(err.Error()), "unknown") {
			return fmt.Errorf("forget %s: %w", s.instName(in), err)
		}
		r.step(step, in.ID, "ok", "")
	}
	return nil
}

func (s *Service) removeScaledInstances(ctx context.Context, r *opRun, unitID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	var insts []*store.Instance
	for _, id := range ids {
		in, err := store.First[store.Instance](s.Store, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if in.UnitID != unitID {
			return invalid("instance %d does not belong to unit %d", id, unitID)
		}
		insts = append(insts, in)
	}
	sort.SliceStable(insts, func(i, j int) bool {
		if insts[i].Desired != insts[j].Desired {
			return insts[i].Desired == "replica"
		}
		return insts[i].ID < insts[j].ID
	})
	for _, in := range insts {
		stopStep := "缩容停止实例 " + s.instName(in)
		if !r.hasSuccessfulStep(stopStep, in.ID) {
			if _, err := s.send(ctx, in.HostID, &agentv1.InstanceAction{Port: in.Port, Action: agentv1.InstanceAction_STOP}); err != nil && !strings.Contains(strings.ToUpper(err.Error()), "NOT_FOUND") {
				r.step(stopStep, in.ID, "failed", err.Error())
				return err
			}
			r.step(stopStep, in.ID, "ok", "")
		}
		deleteStep := "缩容删除实例 " + s.instName(in)
		if !r.hasSuccessfulStep(deleteStep, in.ID) {
			if _, err := s.send(ctx, in.HostID, &agentv1.InstanceAction{Port: in.Port, Action: agentv1.InstanceAction_DELETE}); err != nil && !strings.Contains(strings.ToUpper(err.Error()), "NOT_FOUND") {
				r.step(deleteStep, in.ID, "failed", err.Error())
				return err
			}
			r.step(deleteStep, in.ID, "ok", "")
		}
		_, _ = s.q().Instance.WithContext(ctx).Where(s.q().Instance.ID.Eq(in.ID)).Delete(in)
	}
	return nil
}

func clusterNodeSlotCount(raw []byte, nodeID string) uint32 {
	var n uint32
	for _, rg := range clusterOwnedRanges(raw, nodeID, 16384) {
		n += rg.End - rg.Start + 1
	}
	return n
}

func clusterOwnedRanges(raw []byte, nodeID string, limit uint32) []*agentv1.SlotRange {
	var out []*agentv1.SlotRange
	var remaining = limit
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] != nodeID {
			continue
		}
		for _, token := range fields[8:] {
			if strings.HasPrefix(token, "[") || remaining == 0 {
				continue
			}
			parts := strings.SplitN(token, "-", 2)
			start64, err := parseSlotNumber(parts[0])
			if err != nil || start64 > 16383 {
				continue
			}
			end64 := start64
			if len(parts) == 2 {
				end64, err = parseSlotNumber(parts[1])
				if err != nil || end64 > 16383 || end64 < start64 {
					continue
				}
			}
			length := uint32(end64-start64) + 1
			if length > remaining {
				end64 = start64 + uint64(remaining) - 1
				length = remaining
			}
			out = append(out, &agentv1.SlotRange{Start: uint32(start64), End: uint32(end64)})
			remaining -= length
		}
	}
	return out
}

func parseSlotNumber(raw string) (uint64, error) {
	var n uint64
	if raw == "" {
		return 0, errors.New("empty slot")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid slot")
		}
		n = n*10 + uint64(r-'0')
	}
	return n, nil
}
