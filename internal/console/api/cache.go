package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/orch"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
)

// ---------- 主机 ----------

type hostView struct {
	store.Host
	Online    bool              `json:"online"`
	Tuning    map[string]any    `json:"tuning,omitempty"`
	Warnings  []string          `json:"warnings"`
	Instances []*store.Instance `json:"instances,omitempty"`
}

// tuningWarnings 按缓存服务的最佳实践检查内核参数（规格书 7 节 M3 主机体检）。
func tuningWarnings(raw string) (map[string]any, []string) {
	var t agentv1.HostTuning
	if raw == "" || protojson.Unmarshal([]byte(raw), &t) != nil {
		return nil, nil
	}
	m := map[string]any{"transparent_hugepage": t.TransparentHugepage, "overcommit_memory": t.OvercommitMemory, "somaxconn": t.Somaxconn, "nofile_limit": t.NofileLimit}
	var w []string
	if t.TransparentHugepage != "" && t.TransparentHugepage != "never" {
		w = append(w, "透明大页（THP）未关闭：fork 持久化时会造成延迟抖动与内存放大")
	}
	if t.OvercommitMemory != 1 {
		w = append(w, "vm.overcommit_memory 不是 1：内存紧张时 BGSAVE / AOF 重写可能 fork 失败")
	}
	if t.Somaxconn > 0 && t.Somaxconn < 1024 {
		w = append(w, "net.core.somaxconn 小于 1024：高并发建连可能被丢弃")
	}
	if t.NofileLimit > 0 && t.NofileLimit < 65535 {
		w = append(w, "文件句柄上限小于 65535：限制最大连接数")
	}
	return m, w
}

func (s *Server) hostView(h store.Host) hostView {
	v := hostView{Host: h, Online: s.GW.Online(h.ID), Warnings: []string{}}
	v.Tuning, v.Warnings = tuningWarnings(h.Tuning)
	if v.Warnings == nil {
		v.Warnings = []string{}
	}
	return v
}

func (s *Server) listHosts(c *gin.Context) {
	hq := s.q().Host
	hosts, err := hq.WithContext(c.Request.Context()).Where(hq.Status.Neq("revoked")).Order(hq.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]hostView, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, s.hostView(*h))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getHost(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	h, err := store.First[store.Host](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	v := s.hostView(*h)
	iq := s.q().Instance
	instances, err := iq.WithContext(c.Request.Context()).Where(iq.HostID.Eq(id)).Order(iq.Port).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	v.Instances = instances
	c.JSON(http.StatusOK, v)
}

var zoneRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,64}$`)
var addrRe = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)

func (s *Server) updateHost(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		Zone    *string `json:"zone"`
		Address *string `json:"address"`
		Status  *string `json:"status"` // online 主机可进入 maintenance（不再分配新实例）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	u := map[string]any{}
	if req.Zone != nil {
		if !zoneRe.MatchString(*req.Zone) {
			fail(c, http.StatusBadRequest, "BAD_ZONE", "zone 只能包含字母、数字与 _ . -")
			return
		}
		u["zone"] = *req.Zone
	}
	if req.Address != nil {
		if !addrRe.MatchString(*req.Address) {
			fail(c, http.StatusBadRequest, "BAD_ADDRESS", "地址必须是主机名或 IP")
			return
		}
		u["address"] = *req.Address
	}
	if req.Status != nil {
		if *req.Status != "maintenance" && *req.Status != "online" {
			fail(c, http.StatusBadRequest, "BAD_STATUS", "status 只能是 maintenance 或 online")
			return
		}
		u["status"] = *req.Status
	}
	hq := s.q().Host
	if _, err := hq.WithContext(c.Request.Context()).Where(hq.ID.Eq(id), hq.Status.Neq("revoked")).Updates(u); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "host.update", "host", fmt.Sprint(id), u)
	s.getHost(c)
}

func (s *Server) enrollToken(c *gin.Context) {
	var req struct {
		Zone string `json:"zone"`
	}
	_ = c.ShouldBindJSON(&req)
	if !zoneRe.MatchString(req.Zone) {
		fail(c, http.StatusBadRequest, "BAD_ZONE", "zone 只能包含字母、数字与 _ . -")
		return
	}
	snap := s.Lic.Snapshot()
	if snap.Payload != nil && snap.Payload.Limits.Hosts > 0 {
		hq := s.q().Host
		n, err := hq.WithContext(c.Request.Context()).Where(hq.Status.Neq("revoked")).Count()
		if err != nil {
			failErr(c, err)
			return
		}
		if n >= int64(snap.Payload.Limits.Hosts) {
			fail(c, http.StatusPaymentRequired, "LICENSE_HOST_LIMIT", fmt.Sprintf("已达到授权主机数上限（%d）", snap.Payload.Limits.Hosts))
			return
		}
	}
	tok, exp, err := s.GW.NewEnrollToken(req.Zone, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "host.enroll_token", "host", "", map[string]any{"zone": req.Zone, "expires_at": exp})
	cmd := fmt.Sprintf("sudo -u sycache-agent sycache-agent enroll -server %s -token %s -ca-fingerprint %s && sudo systemctl enable --now sycache-agent", s.GRPCAddr, tok, s.CAFingerprint)
	c.JSON(http.StatusOK, gin.H{"token": tok, "expires_at": exp, "server": s.GRPCAddr, "ca_fingerprint": s.CAFingerprint, "command": cmd})
}

func (s *Server) revokeHost(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	iq := s.q().Instance
	n, err := iq.WithContext(c.Request.Context()).Where(iq.HostID.Eq(id)).Count()
	if err != nil {
		failErr(c, err)
		return
	}
	if n > 0 && c.Query("force") != "1" {
		fail(c, http.StatusConflict, "HOST_IN_USE", fmt.Sprintf("该主机上还有 %d 个实例；先删除或迁移部署单元（吊销不会停止实例，只是控制台不再管理它们）", n))
		return
	}
	hq := s.q().Host
	if _, err := hq.WithContext(c.Request.Context()).Where(hq.ID.Eq(id)).Update(hq.Status, "revoked"); err != nil {
		failErr(c, err)
		return
	}
	s.GW.Disconnect(id)
	s.audit(c, "host.revoke", "host", fmt.Sprint(id), map[string]any{"instances": n})
	c.Status(http.StatusNoContent)
}

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$`)

// switchVersion 切换主机的数据面版本（current 软链）；实例在下次（滚动）重启时使用新版本（规格书 6.4）。
func (s *Server) switchVersion(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !versionRe.MatchString(req.Version) {
		fail(c, http.StatusBadRequest, "BAD_VERSION", "invalid version")
		return
	}
	h, err := store.First[store.Host](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	installed := false
	for _, v := range strings.Split(h.EngineVersions, ",") {
		installed = installed || v == req.Version
	}
	if !installed {
		fail(c, http.StatusConflict, "NOT_INSTALLED", "该版本未安装在此主机上（先安装对应的 sycache 软件包）")
		return
	}
	r, err := s.GW.Send(c.Request.Context(), id, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_SwitchVersion{SwitchVersion: &agentv1.SwitchVersion{Version: req.Version}}}, time.Minute)
	if err != nil {
		failErr(c, err)
		return
	}
	if r.Status != agentv1.CommandResult_OK {
		fail(c, http.StatusConflict, "SWITCH_FAILED", r.Message)
		return
	}
	hq := s.q().Host
	if _, err := hq.WithContext(c.Request.Context()).Where(hq.ID.Eq(id)).Update(hq.EngineCurrent, req.Version); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "host.switch_version", "host", fmt.Sprint(id), map[string]string{"from": h.EngineCurrent, "to": req.Version})
	c.JSON(http.StatusOK, gin.H{"engine_current": req.Version})
}

// ---------- 参数目录与模板 ----------

func (s *Server) paramCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, paramcat.All())
}

type templateView struct {
	store.ParamTemplate
	Params map[string]string `json:"params"`
}

func (s *Server) listTemplates(c *gin.Context) {
	tq := s.q().ParamTemplate
	ts, err := tq.WithContext(c.Request.Context()).Order(tq.Builtin.Desc(), tq.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]templateView, 0, len(ts))
	for _, t := range ts {
		v := templateView{ParamTemplate: *t, Params: map[string]string{}}
		_ = json.Unmarshal([]byte(t.Params), &v.Params)
		out = append(out, v)
	}
	c.JSON(http.StatusOK, out)
}

var tplNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

func (s *Server) saveTemplate(c *gin.Context) {
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Params      map[string]string `json:"params"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if !tplNameRe.MatchString(req.Name) {
		fail(c, http.StatusBadRequest, "BAD_NAME", "模板名只能包含小写字母、数字和连字符")
		return
	}
	params, err := paramcat.NormalizeAll(req.Params)
	if err != nil {
		fail(c, http.StatusBadRequest, "BAD_PARAMS", err.Error())
		return
	}
	if _, ok := params["maxmemory"]; ok {
		fail(c, http.StatusBadRequest, "BAD_PARAMS", "maxmemory 由部署单元的内存规格决定，不能放在模板中")
		return
	}
	b, _ := json.Marshal(params)
	t := store.ParamTemplate{Name: req.Name, Description: req.Description, Params: string(b), UpdatedBy: c.GetString("user"), UpdatedAt: time.Now()}
	if c.Param("id") != "" {
		id, ok := parseID(c)
		if !ok {
			return
		}
		old, err := store.First[store.ParamTemplate](s.Store, id)
		if err != nil {
			failErr(c, err)
			return
		}
		if old.Builtin {
			fail(c, http.StatusForbidden, "BUILTIN", "内置模板不可修改，可另存为新模板")
			return
		}
		t.ID = id
	}
	if err := s.q().ParamTemplate.WithContext(c.Request.Context()).Save(&t); err != nil {
		fail(c, http.StatusConflict, "DUPLICATE", "模板名已存在")
		return
	}
	s.audit(c, "template.save", "param_template", fmt.Sprint(t.ID), params)
	c.JSON(http.StatusOK, templateView{ParamTemplate: t, Params: params})
}

func (s *Server) deleteTemplate(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	t, err := store.First[store.ParamTemplate](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	if t.Builtin {
		fail(c, http.StatusForbidden, "BUILTIN", "内置模板不可删除")
		return
	}
	if _, err := s.q().ParamTemplate.WithContext(c.Request.Context()).Where(s.q().ParamTemplate.ID.Eq(id)).Delete(t); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "template.delete", "param_template", fmt.Sprint(id), nil)
	c.Status(http.StatusNoContent)
}

// ---------- 部署单元 ----------

type instanceView struct {
	store.Instance
	Host string `json:"host"`
	Addr string `json:"address"`
}

type unitView struct {
	store.Unit
	Instances []instanceView     `json:"instances"`
	Busy      uint               `json:"busy_operation"` // 进行中的变更 ID，0 表示空闲
	Endpoint  string             `json:"endpoint"`       // 主节点地址
	Params    map[string]string  `json:"params,omitempty"`
	LastOp    *store.Operation   `json:"last_operation,omitempty"`
	Health    string             `json:"health"` // ok | warning | critical
	Problems  []string           `json:"problems"`
	Accounts  []store.AclAccount `json:"accounts,omitempty"`
	Usage     map[string]any     `json:"usage"`
}

func (s *Server) unitView(u store.Unit, detail bool) unitView {
	v := unitView{Unit: u, Instances: []instanceView{}, Problems: []string{}, Usage: map[string]any{}}
	v.Busy, _ = s.Store.UnitLockOwner(u.ID)
	iq := s.q().Instance
	insts, _ := iq.WithContext(bg).Where(iq.UnitID.Eq(u.ID)).Order(iq.ID).Find()
	hosts := map[uint]store.Host{}
	var used, ops, keys uint64
	for _, in := range insts {
		h, ok := hosts[in.HostID]
		if !ok {
			if hp, err := store.First[store.Host](s.Store, in.HostID); err == nil {
				h = *hp
			}
			hosts[in.HostID] = h
		}
		iv := instanceView{Instance: *in, Host: h.Name, Addr: fmt.Sprintf("%s:%d", h.Address, in.Port)}
		v.Instances = append(v.Instances, iv)
		if in.Role == "master" || (in.Role == "" && in.Desired == "master") {
			v.Endpoint = iv.Addr
			used, ops, keys = in.UsedMemory, in.OpsPerSec, in.Keys
		}
		switch {
		case in.State == "failed" || in.State == "missing":
			v.Problems = append(v.Problems, fmt.Sprintf("%s 状态 %s %s", iv.Addr, in.State, in.Error))
		case u.Status != "stopped" && in.State == "stopped":
			v.Problems = append(v.Problems, iv.Addr+" 已停止")
		case in.Role == "slave" && in.LinkStatus != "up":
			v.Problems = append(v.Problems, iv.Addr+" 复制链路断开")
		case in.Drifted:
			v.Problems = append(v.Problems, iv.Addr+" 参数与当前版本不一致（漂移）")
		case in.PendingRestart:
			v.Problems = append(v.Problems, iv.Addr+" 有参数等待重启生效")
		}
		if h.Status != "online" && h.Status != "maintenance" {
			v.Problems = append(v.Problems, fmt.Sprintf("主机 %s 离线", h.Name))
		}
	}
	v.Usage["used_memory"], v.Usage["ops_per_sec"], v.Usage["keys"] = used, ops, keys
	v.Health = "ok"
	if len(v.Problems) > 0 {
		v.Health = "warning"
		for _, p := range v.Problems {
			if strings.Contains(p, "failed") || strings.Contains(p, "missing") || strings.Contains(p, "离线") {
				v.Health = "critical"
			}
		}
	}
	if detail {
		rq := s.q().ParamRevision
		rev, err := rq.WithContext(bg).Where(rq.ID.Eq(u.RevisionID)).First()
		if err == nil {
			v.Params = map[string]string{}
			_ = json.Unmarshal([]byte(rev.Params), &v.Params)
		}
		oq := s.q().Operation
		if op, err := oq.WithContext(bg).Where(oq.UnitID.Eq(u.ID)).Order(oq.ID.Desc()).First(); err == nil {
			v.LastOp = op
		}
		aq := s.q().AclAccount
		accounts, _ := aq.WithContext(bg).Where(aq.UnitID.Eq(u.ID)).Order(aq.Name).Find()
		for _, account := range accounts {
			v.Accounts = append(v.Accounts, *account)
		}
	}
	return v
}

func (s *Server) listUnits(c *gin.Context) {
	uq := s.q().Unit
	units, err := uq.WithContext(c.Request.Context()).Order(uq.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]unitView, 0, len(units))
	for _, u := range units {
		out = append(out, s.unitView(*u, false))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getUnit(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	u, err := store.First[store.Unit](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, s.unitView(*u, true))
}

func (s *Server) createUnit(c *gin.Context) {
	var spec orch.UnitSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		failErr(c, err)
		return
	}
	if spec.Mode == "sentinel" && !s.Lic.Snapshot().Feature("sentinel") || spec.Mode == "cluster" && !s.Lic.Snapshot().Feature("cluster") {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含 "+spec.Mode+" 形态")
		return
	}
	u, op, err := s.Orch.CreateUnit(spec, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.create", "unit", fmt.Sprint(u.ID), gin.H{"name": u.Name, "mode": u.Mode, "memory_mb": u.MemoryMB, "replicas": u.Replicas, "operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"unit": u, "operation": op})
}

func (s *Server) deleteUnit(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	u, err := store.First[store.Unit](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	if c.Query("confirm") != u.Name {
		fail(c, http.StatusBadRequest, "CONFIRM_REQUIRED", "删除部署单元需要在 confirm 参数中填写单元名称")
		return
	}
	op, err := s.Orch.DeleteUnit(id, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.delete", "unit", fmt.Sprint(id), gin.H{"name": u.Name, "operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

func (s *Server) unitAction(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		Switchover bool `json:"switchover"`
	}
	_ = c.ShouldBindJSON(&req)
	action := c.Param("action")
	op, err := s.Orch.Action(id, action, req.Switchover, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit."+action, "unit", fmt.Sprint(id), gin.H{"operation": op.ID, "switchover": req.Switchover})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

func (s *Server) failover(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	op, err := s.Orch.Failover(id, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.failover", "unit", fmt.Sprint(id), gin.H{"operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

type revisionView struct {
	store.ParamRevision
	Params map[string]string `json:"params"`
}

func (s *Server) listRevisions(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	rq := s.q().ParamRevision
	revs, err := rq.WithContext(c.Request.Context()).Where(rq.UnitID.Eq(id)).Order(rq.Seq.Desc()).Limit(50).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]revisionView, 0, len(revs))
	for _, r := range revs {
		v := revisionView{ParamRevision: *r, Params: map[string]string{}}
		_ = json.Unmarshal([]byte(r.Params), &v.Params)
		out = append(out, v)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) previewParams(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var ch orch.ParamChange
	if err := c.ShouldBindJSON(&ch); err != nil {
		failErr(c, err)
		return
	}
	pv, _, err := s.Orch.PreviewParams(id, ch)
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, pv)
}

func (s *Server) applyParams(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var ch orch.ParamChange
	if err := c.ShouldBindJSON(&ch); err != nil {
		failErr(c, err)
		return
	}
	op, err := s.Orch.ApplyParams(id, ch, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.params", "unit", fmt.Sprint(id), gin.H{"set": ch.Set, "reset": ch.Reset, "note": ch.Note, "restart": ch.Restart, "operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

type metricPoint struct {
	Minute     int64   `json:"minute"`
	OpsPerSec  uint64  `json:"ops_per_sec"`
	UsedMemory uint64  `json:"used_memory"`
	Clients    uint64  `json:"clients"`
	HitRate    float64 `json:"hit_rate"`
}

// unitMetrics 返回部署单元按分钟的监控序列（主节点的 ops / 内存 / 连接，命中率按分钟增量计算）。
func (s *Server) unitMetrics(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	minutes, _ := strconv.Atoi(c.DefaultQuery("minutes", "60"))
	if minutes <= 0 || minutes > 7*24*60 {
		minutes = 60
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	iq := s.q().Instance
	var ids []uint
	if err := iq.WithContext(c.Request.Context()).Where(iq.UnitID.Eq(id)).Pluck(iq.ID, &ids); err != nil {
		failErr(c, err)
		return
	}
	out := map[uint][]metricPoint{}
	if len(ids) > 0 {
		mq := s.q().MetricMinute
		rows, err := mq.WithContext(c.Request.Context()).Where(mq.InstanceID.In(ids...), mq.Minute.Gte(since)).Order(mq.Minute).Find()
		if err != nil {
			failErr(c, err)
			return
		}
		last := map[uint]store.MetricMinute{}
		for _, r := range rows {
			p := metricPoint{Minute: r.Minute, OpsPerSec: r.OpsPerSec, UsedMemory: r.UsedMemory, Clients: r.Clients}
			if prev, ok := last[r.InstanceID]; ok && r.Hits >= prev.Hits && r.Misses >= prev.Misses {
				if d := (r.Hits - prev.Hits) + (r.Misses - prev.Misses); d > 0 {
					p.HitRate = float64(r.Hits-prev.Hits) / float64(d)
				}
			}
			last[r.InstanceID] = *r
			out[r.InstanceID] = append(out[r.InstanceID], p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"instances": out})
}

func (s *Server) listAccounts(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	aq := s.q().AclAccount
	as, err := aq.WithContext(c.Request.Context()).Where(aq.UnitID.Eq(id)).Order(aq.Name).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, as)
}

func (s *Server) saveAccount(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var spec orch.AccountSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		failErr(c, err)
		return
	}
	gen, err := s.Orch.SaveAccount(c.Request.Context(), id, spec, c.GetString("user"))
	// 口令不写入审计
	s.audit(c, "unit.account.save", "unit", fmt.Sprint(id), gin.H{"name": spec.Name, "key_patterns": spec.KeyPatterns, "categories": spec.Categories,
		"enabled": spec.Enabled, "password_changed": spec.Password != "" || gen != "", "ok": err == nil})
	if err != nil {
		failErr(c, err)
		return
	}
	resp := gin.H{"name": spec.Name}
	if gen != "" {
		resp["generated_password"] = gen // 只返回这一次
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) deleteAccount(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if err := s.Orch.DeleteAccount(c.Request.Context(), id, name); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.account.delete", "unit", fmt.Sprint(id), gin.H{"name": name})
	c.Status(http.StatusNoContent)
}

type opView struct {
	store.Operation
	Steps []orch.Step `json:"steps"`
	Unit  string      `json:"unit"`
}

func (s *Server) opView(op store.Operation) opView {
	v := opView{Operation: op, Steps: []orch.Step{}}
	_ = json.Unmarshal([]byte(op.Steps), &v.Steps)
	if u, err := store.First[store.Unit](s.Store, op.UnitID); err == nil {
		v.Unit = u.Name
	}
	return v
}

func (s *Server) listOperations(c *gin.Context) {
	oq := s.q().Operation.WithContext(c.Request.Context()).Order(s.q().Operation.ID.Desc()).Limit(100)
	if uid, _ := strconv.Atoi(c.Query("unit_id")); uid > 0 {
		oq = oq.Where(s.q().Operation.UnitID.Eq(uint(uid)))
	}
	ops, err := oq.Find()
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]opView, 0, len(ops))
	for _, op := range ops {
		out = append(out, s.opView(*op))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getOperation(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	op, err := store.First[store.Operation](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, s.opView(*op))
}

// ---------- 总览 ----------

func (s *Server) dashboard(c *gin.Context) {
	hq := s.q().Host
	hosts, _ := hq.WithContext(c.Request.Context()).Where(hq.Status.Neq("revoked")).Find()
	online, warnHosts := 0, 0
	for _, h := range hosts {
		if s.GW.Online(h.ID) {
			online++
		}
		if _, w := tuningWarnings(h.Tuning); len(w) > 0 {
			warnHosts++
		}
	}
	uq := s.q().Unit
	units, _ := uq.WithContext(c.Request.Context()).Order(uq.ID).Find()
	health := map[string]int{"ok": 0, "warning": 0, "critical": 0}
	var problems []gin.H
	var memTotal, memUsed, ops uint64
	for _, u := range units {
		v := s.unitView(*u, false)
		health[v.Health]++
		for _, p := range v.Problems {
			problems = append(problems, gin.H{"unit": u.Name, "unit_id": u.ID, "problem": p})
		}
		memTotal += uint64(u.MemoryMB) << 20
		for _, in := range v.Instances {
			if in.Role == "master" {
				memUsed += in.UsedMemory
				ops += in.OpsPerSec
			}
		}
	}
	sort.SliceStable(problems, func(i, j int) bool { return fmt.Sprint(problems[i]["unit"]) < fmt.Sprint(problems[j]["unit"]) })
	var instCount int64
	instCount, _ = s.q().Instance.WithContext(c.Request.Context()).Count()
	opq := s.q().Operation
	ops6, _ := opq.WithContext(c.Request.Context()).Order(opq.ID.Desc()).Limit(6).Find()
	recent := make([]opView, 0, len(ops6))
	for _, op := range ops6 {
		recent = append(recent, s.opView(*op))
	}
	c.JSON(http.StatusOK, gin.H{
		"hosts": len(hosts), "hosts_online": online, "hosts_tuning_warnings": warnHosts,
		"units": len(units), "instances": instCount, "unit_health": health, "problems": problems,
		"memory_total": memTotal, "memory_used": memUsed, "ops_per_sec": ops,
		"operations": recent, "license": s.licView(),
	})
}
