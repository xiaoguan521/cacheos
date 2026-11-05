package instance

import (
	"context"
	"os"
	"strconv"
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/resp"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
)

// managedNames 是全部受管参数名（上报 config 与计算摘要用）；不含口令类参数。
var managedNames = func() []string {
	var out []string
	for _, p := range paramcat.All() {
		out = append(out, p.Name)
	}
	return out
}()

// Status 采集一个实例的状态；访问失败时 state 为 STOPPED 或 FAILED 并带原因。
func (m *Manager) Status(ctx context.Context, port uint32) *agentv1.InstanceStatus {
	st, err := m.Load(port)
	if err != nil {
		return &agentv1.InstanceStatus{Port: port, State: agentv1.State_STATE_FAILED, Error: err.Error()}
	}
	s := &agentv1.InstanceStatus{Port: port, Kind: st.Kind}
	c, err := m.Dial(ctx, st)
	if err != nil {
		if !m.running(ctx, st) {
			s.State = agentv1.State_STATE_STOPPED
		} else {
			s.State = agentv1.State_STATE_FAILED
			s.Error = err.Error()
		}
		return s
	}
	defer c.Close()
	v, err := c.Do(ctx, "INFO", "everything")
	if err != nil {
		if resp.IsError(err, "LOADING") {
			s.State = agentv1.State_STATE_LOADING
			return s
		}
		s.State = agentv1.State_STATE_FAILED
		s.Error = err.Error()
		return s
	}
	fill(s, resp.ParseInfo(v.Text()))
	s.State = agentv1.State_STATE_RUNNING
	if s.Error != "" {
		s.State = agentv1.State_STATE_LOADING
	}
	if st.Kind == agentv1.Kind_KIND_SENTINEL {
		return s
	}
	if cfg, err := configGet(ctx, c, managedNames); err == nil {
		s.Config = cfg
		s.ConfigDigest = Digest(cfg)
	}
	if len(st.Pending) > 0 {
		s.State = agentv1.State_STATE_PENDING_RESTART
	}
	if sl, err := c.Do(ctx, "SLOWLOG", "GET", "10"); err == nil {
		s.SlowlogTail = slowlog(sl)
	}
	if lt, err := c.Do(ctx, "LATENCY", "LATEST"); err == nil {
		for _, e := range lt.Array {
			if len(e.Array) >= 4 && e.Array[3].Int > s.LatencyMaxMs {
				s.LatencyMaxMs = e.Array[3].Int
			}
		}
	}
	return s
}

// Drift 返回实际值与当前参数版本不一致的参数（名称 → 实际值）。等待重启的参数不算漂移。
func (m *Manager) Drift(st *State, s *agentv1.InstanceStatus) map[string]string {
	out := map[string]string{}
	if s.GetConfig() == nil {
		return out
	}
	for k, want := range st.Params {
		if _, pending := st.Pending[k]; pending {
			continue
		}
		if got, ok := s.Config[k]; ok && !Equal(k, got, want) {
			out[k] = got
		}
	}
	return out
}

func fill(s *agentv1.InstanceStatus, i map[string]string) {
	s.Role = i["role"]
	s.RunId = i["run_id"]
	s.UptimeSeconds = atou(i["uptime_in_seconds"])
	s.EngineVersion = i["sycache_version"]
	s.UsedMemory = atou(i["used_memory"])
	s.Maxmemory = atou(i["maxmemory"])
	s.MemFragmentationRatio = atof(i["mem_fragmentation_ratio"])
	s.ConnectedClients = atou(i["connected_clients"])
	s.OpsPerSec = atou(i["instantaneous_ops_per_sec"])
	s.KeyspaceHits = atou(i["keyspace_hits"])
	s.KeyspaceMisses = atou(i["keyspace_misses"])
	s.ExpiredKeys = atou(i["expired_keys"])
	s.EvictedKeys = atou(i["evicted_keys"])
	s.TotalNetInputBytes = atou(i["total_net_input_bytes"])
	s.TotalNetOutputBytes = atou(i["total_net_output_bytes"])
	for k, v := range i {
		if strings.HasPrefix(k, "db") {
			s.Keys += atou(resp.ParseFields(v)["keys"])
		}
	}
	s.MasterHost = i["master_host"]
	s.MasterPort = uint32(atou(i["master_port"]))
	s.MasterLinkStatus = i["master_link_status"]
	s.MasterReplOffset = atoi(i["master_repl_offset"])
	s.ConnectedSlaves = uint32(atou(i["connected_slaves"]))
	for n := 0; ; n++ {
		v, ok := i["slave"+strconv.Itoa(n)]
		if !ok {
			break
		}
		f := resp.ParseFields(v)
		off := atoi(f["offset"])
		s.Replicas = append(s.Replicas, &agentv1.ReplicaInfo{Ip: f["ip"], Port: uint32(atou(f["port"])), State: f["state"], Offset: off, Lag: s.MasterReplOffset - off})
	}
	s.RdbLastBgsaveStatus = i["rdb_last_bgsave_status"]
	s.RdbLastSaveTime = atoi(i["rdb_last_save_time"])
	s.AofLastWriteStatus = i["aof_last_write_status"]
	s.AofEnabled = i["aof_enabled"] == "1"
	s.ClusterEnabled = i["cluster_enabled"] == "1"
	if i["loading"] == "1" {
		s.Error = "loading dataset"
	}
	for n := 0; ; n++ {
		v, ok := i["master"+strconv.Itoa(n)]
		if !ok {
			break
		}
		f := resp.ParseFields(v)
		addr := f["address"]
		host, port := addr, ""
		if j := strings.LastIndexByte(addr, ':'); j > 0 {
			host, port = addr[:j], addr[j+1:]
		}
		s.SentinelMasters = append(s.SentinelMasters, &agentv1.SentinelMaster{Name: f["name"], Ip: host, Port: uint32(atou(port)), Flags: f["status"],
			NumSlaves: uint32(atou(f["slaves"])), NumOtherSentinels: uint32(atou(f["sentinels"]))})
	}
	if i["sentinel_masters"] != "" {
		s.Role = "sentinel"
	}
}

// slowlog 只保留命令名与第一个参数（通常是键名），其余参数（可能含 value）一律丢弃。
func slowlog(v resp.Value) []*agentv1.SlowlogEntry {
	var out []*agentv1.SlowlogEntry
	for _, e := range v.Array {
		if len(e.Array) < 4 {
			continue
		}
		args := e.Array[3].Strings()
		cmd := ""
		if len(args) > 0 {
			cmd = args[0]
			if len(args) > 1 {
				k := args[1]
				if len(k) > 64 {
					k = k[:64] + "…"
				}
				cmd += " " + k
			}
		}
		ent := &agentv1.SlowlogEntry{Id: e.Array[0].Int, Timestamp: e.Array[1].Int, DurationUs: e.Array[2].Int, Command: cmd}
		if len(e.Array) > 4 {
			ent.Client = e.Array[4].Str
		}
		out = append(out, ent)
	}
	return out
}

// InstalledVersions 列出 versions 目录下的版本与 current 指向的版本。
func (l Layout) InstalledVersions() (all []string, current string) {
	ents, _ := os.ReadDir(l.Root + "/versions")
	for _, e := range ents {
		all = append(all, e.Name())
	}
	if t, err := os.Readlink(l.Root + "/current"); err == nil {
		current = strings.TrimPrefix(t, "versions/")
	}
	return all, current
}
