package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
)

// promContentType 是 Prometheus 文本格式 0.0.4 的 Content-Type。
const promContentType = "text/plain; version=0.0.4; charset=utf-8"

// promLabel / promSample / promFamily 是手写文本输出所需的最小模型。
type promLabel struct{ Name, Value string }

type promSample struct {
	Labels []promLabel
	Value  float64
}

type promFamily struct {
	Name    string
	Help    string
	Type    string // gauge | counter
	Samples []promSample
}

// promEscapeLabel 按文本格式转义标签值：反斜杠、双引号、换行。
func promEscapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// promEscapeHelp 转义 HELP 文本：反斜杠与换行。
func promEscapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// renderProm 把指标族渲染为文本格式。
func renderProm(fams []promFamily) []byte {
	var b strings.Builder
	for _, f := range fams {
		b.WriteString("# HELP " + f.Name + " " + promEscapeHelp(f.Help) + "\n")
		b.WriteString("# TYPE " + f.Name + " " + f.Type + "\n")
		for _, s := range f.Samples {
			b.WriteString(f.Name)
			if len(s.Labels) > 0 {
				b.WriteByte('{')
				for i, l := range s.Labels {
					if i > 0 {
						b.WriteByte(',')
					}
					b.WriteString(l.Name + `="` + promEscapeLabel(l.Value) + `"`)
				}
				b.WriteByte('}')
			}
			b.WriteByte(' ')
			b.WriteString(strconv.FormatFloat(s.Value, 'f', -1, 64))
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

func promBool(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// buildPromFamilies 只读取控制台已保存的状态：主机行上的心跳字段、实例行的状态列与最近一次 InstanceStatus（JSON）。
// online 为主机当前是否连接；离线主机上的实例 cacheos_instance_up 为 0，其余数值为最后一次上报的值。
// 不输出任何口令、账号、key 名或令牌。
func buildPromFamilies(hosts []store.Host, online func(uint) bool, unitNames map[uint]string, insts []store.Instance) []promFamily {
	hostByID := map[uint]store.Host{}
	hostUp := map[uint]bool{}
	for _, h := range hosts {
		hostByID[h.ID] = h
		hostUp[h.ID] = online(h.ID)
	}

	hostFam := []promFamily{
		{Name: "cacheos_host_up", Help: "主机当前是否与控制台保持连接（1 在线，0 离线）", Type: "gauge"},
		{Name: "cacheos_host_cpu_percent", Help: "主机 CPU 使用率（百分比，最后一次心跳）", Type: "gauge"},
		{Name: "cacheos_host_memory_used_bytes", Help: "主机已用内存字节数（最后一次心跳）", Type: "gauge"},
		{Name: "cacheos_host_memory_total_bytes", Help: "主机内存总字节数（最后一次心跳）", Type: "gauge"},
		{Name: "cacheos_host_data_disk_free_bytes", Help: "数据盘可用字节数（最后一次心跳）", Type: "gauge"},
	}
	for _, h := range hosts {
		hl := []promLabel{{"host", h.Name}}
		hostFam[0].Samples = append(hostFam[0].Samples, promSample{hl, promBool(hostUp[h.ID])})
		if h.LastSeenAt == nil {
			continue // 从未收到心跳：资源数值不存在，不输出 0 以免被当作真实数据
		}
		hostFam[1].Samples = append(hostFam[1].Samples, promSample{hl, h.CPUPercent})
		hostFam[2].Samples = append(hostFam[2].Samples, promSample{hl, float64(h.MemUsedBytes)})
		hostFam[3].Samples = append(hostFam[3].Samples, promSample{hl, float64(h.MemTotalBytes)})
		hostFam[4].Samples = append(hostFam[4].Samples, promSample{hl, float64(h.DiskFreeBytes)})
	}

	instFam := []promFamily{
		{Name: "cacheos_instance_up", Help: "实例是否存活：主机在线且状态为 running、loading 或 pending_restart 时为 1", Type: "gauge"},
		{Name: "cacheos_instance_used_memory_bytes", Help: "实例已用内存字节数", Type: "gauge"},
		{Name: "cacheos_instance_maxmemory_bytes", Help: "实例 maxmemory 字节数，0 表示未设上限", Type: "gauge"},
		{Name: "cacheos_instance_connected_clients", Help: "实例当前连接数", Type: "gauge"},
		{Name: "cacheos_instance_ops_per_sec", Help: "实例每秒操作数", Type: "gauge"},
		{Name: "cacheos_instance_keyspace_hits_total", Help: "键空间命中次数（自实例启动累计）", Type: "counter"},
		{Name: "cacheos_instance_keyspace_misses_total", Help: "键空间未命中次数（自实例启动累计）", Type: "counter"},
		{Name: "cacheos_instance_keys", Help: "实例键数量（各库之和）", Type: "gauge"},
		{Name: "cacheos_instance_mem_fragmentation_ratio", Help: "内存碎片率", Type: "gauge"},
		{Name: "cacheos_instance_repl_offset", Help: "主节点复制偏移（master_repl_offset）", Type: "gauge"},
		{Name: "cacheos_instance_replication_lag_max_bytes", Help: "主节点视角下所有从节点的最大复制延迟字节数", Type: "gauge"},
		{Name: "cacheos_instance_replica_link_up", Help: "从节点与主节点的复制链路是否为 up（1 是，0 否）", Type: "gauge"},
		{Name: "cacheos_instance_last_bgsave_ok", Help: "最近一次 RDB 持久化是否成功（1 成功，0 失败）", Type: "gauge"},
		{Name: "cacheos_instance_last_aof_write_ok", Help: "最近一次 AOF 写入是否成功（仅开启 AOF 的实例，1 成功，0 失败）", Type: "gauge"},
	}
	const (
		fUp = iota
		fUsed
		fMax
		fClients
		fOps
		fHits
		fMisses
		fKeys
		fFrag
		fOffset
		fLag
		fLink
		fBgsave
		fAof
	)

	for _, in := range insts {
		h, ok := hostByID[in.HostID]
		if !ok {
			continue // 已吊销主机上的实例不输出
		}
		labels := []promLabel{{"unit", unitNames[in.UnitID]}, {"host", h.Name}, {"port", strconv.FormatUint(uint64(in.Port), 10)}, {"role", in.Role}}
		alive := hostUp[in.HostID] && (in.State == "running" || in.State == "loading" || in.State == "pending_restart")
		add := func(f int, v float64) {
			instFam[f].Samples = append(instFam[f].Samples, promSample{labels, v})
		}
		add(fUp, promBool(alive))
		if in.Status == "" || in.Kind != "server" {
			continue
		}
		var st agentv1.InstanceStatus
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(in.Status), &st); err != nil {
			continue
		}
		add(fUsed, float64(st.UsedMemory))
		add(fMax, float64(st.Maxmemory))
		add(fClients, float64(st.ConnectedClients))
		add(fOps, float64(st.OpsPerSec))
		add(fHits, float64(st.KeyspaceHits))
		add(fMisses, float64(st.KeyspaceMisses))
		add(fKeys, float64(st.Keys))
		if st.MemFragmentationRatio > 0 {
			add(fFrag, st.MemFragmentationRatio)
		}
		add(fOffset, float64(st.MasterReplOffset))
		if len(st.Replicas) > 0 {
			var maxLag int64
			for _, r := range st.Replicas {
				if r.Lag > maxLag {
					maxLag = r.Lag
				}
			}
			add(fLag, float64(maxLag))
		}
		if st.MasterLinkStatus != "" {
			add(fLink, promBool(st.MasterLinkStatus == "up"))
		}
		if st.RdbLastBgsaveStatus != "" {
			add(fBgsave, promBool(st.RdbLastBgsaveStatus == "ok"))
		}
		if st.AofEnabled && st.AofLastWriteStatus != "" {
			add(fAof, promBool(st.AofLastWriteStatus == "ok"))
		}
	}

	return append(hostFam, instFam...)
}

// prometheusMetrics 输出 Prometheus 文本格式指标。路由挂在 /api/v1 下，沿用登录或 Bearer 令牌鉴权，最低只读角色。
func (s *Server) prometheusMetrics(c *gin.Context) {
	ctx := c.Request.Context()
	hq := s.q().Host
	hostPtrs, err := hq.WithContext(ctx).Where(hq.Status.Neq("revoked")).Order(hq.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	hosts := make([]store.Host, 0, len(hostPtrs))
	for _, h := range hostPtrs {
		hosts = append(hosts, *h)
	}
	uq := s.q().Unit
	units, err := uq.WithContext(ctx).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	unitNames := make(map[uint]string, len(units))
	for _, u := range units {
		unitNames[u.ID] = u.Name
	}
	iq := s.q().Instance
	instPtrs, err := iq.WithContext(ctx).Order(iq.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	insts := make([]store.Instance, 0, len(instPtrs))
	for _, in := range instPtrs {
		insts = append(insts, *in)
	}
	body := renderProm(buildPromFamilies(hosts, s.GW.Online, unitNames, insts))
	c.Data(http.StatusOK, promContentType, body)
}
