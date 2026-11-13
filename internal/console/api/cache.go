package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/gateway"
	"github.com/xiaoguan521/cacheos/internal/console/orch"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
	"github.com/xiaoguan521/cacheos/internal/webcli"
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
	// 单引号包裹防止 shell 特殊字符（服务器地址可能包含 IPv6 方括号、端口号等）
	cmd := fmt.Sprintf("sudo -u sycache-agent sycache-agent enroll -server '%s' -token '%s' -ca-fingerprint '%s' && sudo systemctl enable --now sycache-agent", s.GRPCAddr, tok, s.CAFingerprint)
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

var backupCronRe = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

// switchVersion 切换主机的数据面版本（current 软链）；实例在下次（滚动）重启时使用新版本（规格书 6.4）。
func (s *Server) switchVersion(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !pkgsig.VersionRe.MatchString(req.Version) {
		fail(c, http.StatusBadRequest, "BAD_VERSION", "invalid version")
		return
	}
	h, err := store.First[store.Host](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	installed := h.EngineCurrent == req.Version
	for _, v := range strings.Split(h.EngineVersions, ",") {
		installed = installed || strings.TrimSpace(v) == req.Version
	}
	switchReq := &agentv1.SwitchVersion{Version: req.Version}
	if !installed {
		arch := h.Arch
		if arch == "x86_64" {
			arch = "amd64"
		} else if arch == "aarch64" {
			arch = "arm64"
		}
		pq := s.q().EnginePackage
		pkg, pkgErr := pq.WithContext(c.Request.Context()).Where(pq.Name.Eq("sycache"), pq.Version.Eq(req.Version), pq.Arch.Eq(arch)).First()
		if pkgErr != nil {
			fail(c, http.StatusConflict, "NOT_INSTALLED", "该版本未安装在此主机上，且版本库没有匹配架构的签名包")
			return
		}
		switchReq.Arch = pkg.Arch
		switchReq.Sha256 = pkg.SHA256
		switchReq.VendorSignature = []byte(pkg.Sig)
	}
	r, err := s.GW.Send(c.Request.Context(), id, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_SwitchVersion{SwitchVersion: switchReq}}, time.Minute)
	if err != nil {
		failErr(c, err)
		return
	}
	if r.Status != agentv1.CommandResult_OK {
		fail(c, http.StatusConflict, "SWITCH_FAILED", r.Message)
		return
	}
	hq := s.q().Host
	versions := h.EngineVersions
	if !installed {
		items := make([]string, 0, 4)
		for _, v := range strings.Split(versions, ",") {
			if v = strings.TrimSpace(v); v != "" {
				items = append(items, v)
			}
		}
		items = append(items, req.Version)
		versions = strings.Join(items, ",")
	}
	if _, err := hq.WithContext(c.Request.Context()).Where(hq.ID.Eq(id)).UpdateSimple(hq.EngineCurrent.Value(req.Version), hq.EngineVersions.Value(versions)); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "host.switch_version", "host", fmt.Sprint(id), map[string]string{"from": h.EngineCurrent, "to": req.Version})
	c.JSON(http.StatusOK, gin.H{"engine_current": req.Version})
}

// agentPackageName 是版本库中 agent 自升级包的名称（与 pkgsig.Names 一致）。
const agentPackageName = "sycache-agent"

// agentVersionNewer 与 agent 端 selfupdate 的比较规则一致：比较 N.N.N，再比较 "-" 后的数字。
// 相等或更低都返回 false；agent 只会切换到比正在运行的版本更新的包。
func agentVersionNewer(target, current string) bool {
	pt, pc := agentVersionParts(target), agentVersionParts(current)
	for i := range pt {
		if pt[i] != pc[i] {
			return pt[i] > pc[i]
		}
	}
	return false
}

func agentVersionParts(v string) [4]int {
	var out [4]int
	main, rel, _ := strings.Cut(v, "-")
	for i, s := range strings.SplitN(main, ".", 3) {
		out[i], _ = strconv.Atoi(s)
	}
	out[3], _ = strconv.Atoi(rel)
	return out
}

// upgradeAgent 向在线主机下发 agent 自升级指令（UpgradeAgent）。agent 验签、落盘并退出，由 systemd 重启到新版本；
// 实例进程不受影响。版本库中的包必须已通过厂商签名导入，agent 使用前还会再次校验。
func (s *Server) upgradeAgent(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !pkgsig.VersionRe.MatchString(req.Version) {
		fail(c, http.StatusBadRequest, "BAD_VERSION", "invalid version")
		return
	}
	h, err := store.First[store.Host](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	if h.Status == "offline" || h.Status == "revoked" {
		fail(c, http.StatusConflict, "HOST_OFFLINE", "主机离线，无法下发 agent 升级")
		return
	}
	if !pkgsig.VersionRe.MatchString(h.AgentVersion) {
		fail(c, http.StatusConflict, "AGENT_VERSION_UNKNOWN", "主机未上报可识别的 agent 版本")
		return
	}
	if !agentVersionNewer(req.Version, h.AgentVersion) {
		fail(c, http.StatusConflict, "VERSION_NOT_NEWER", fmt.Sprintf("目标版本 %s 不高于当前 agent 版本 %s；不允许降级或重复安装", req.Version, h.AgentVersion))
		return
	}
	arch := h.Arch
	if arch == "x86_64" {
		arch = "amd64"
	} else if arch == "aarch64" {
		arch = "arm64"
	}
	pq := s.q().EnginePackage
	ctx := c.Request.Context()
	if n, err := pq.WithContext(ctx).Where(pq.Name.Eq(agentPackageName), pq.Version.Eq(req.Version)).Count(); err != nil {
		failErr(c, err)
		return
	} else if n == 0 {
		fail(c, http.StatusNotFound, "AGENT_PACKAGE_NOT_FOUND", "版本库中没有该版本的 agent 包")
		return
	}
	pkg, err := pq.WithContext(ctx).Where(pq.Name.Eq(agentPackageName), pq.Version.Eq(req.Version), pq.Arch.Eq(arch)).First()
	if err != nil {
		fail(c, http.StatusConflict, "ARCH_MISMATCH", "版本库中的 agent 包架构与主机不匹配")
		return
	}
	send := s.sendCmd
	if send == nil {
		send = s.GW.Send
	}
	m := &agentv1.ServerMessage{Body: &agentv1.ServerMessage_UpgradeAgent{UpgradeAgent: &agentv1.UpgradeAgent{
		Version: pkg.Version, Arch: pkg.Arch, Sha256: pkg.SHA256, VendorSignature: []byte(pkg.Sig),
	}}}
	// agent 需要先从控制台分片下载升级包（最多 2 分钟），与 switch-version 的等待上限分开。
	r, err := send(ctx, id, m, 3*time.Minute)
	detail := func(result, reason string) map[string]string {
		return map[string]string{"from": h.AgentVersion, "to": req.Version, "result": result, "reason": reason}
	}
	switch {
	case errors.Is(err, gateway.ErrOffline):
		s.audit(c, "host.upgrade_agent", "host", fmt.Sprint(id), detail("failed", "host offline"))
		fail(c, http.StatusConflict, "HOST_OFFLINE", "主机离线，无法下发 agent 升级")
		return
	case err != nil:
		s.audit(c, "host.upgrade_agent", "host", fmt.Sprint(id), detail("error", err.Error()))
		failErr(c, err)
		return
	case r.Status != agentv1.CommandResult_OK:
		s.audit(c, "host.upgrade_agent", "host", fmt.Sprint(id), detail("failed", r.Message))
		fail(c, http.StatusConflict, "UPGRADE_FAILED", r.Message)
		return
	}
	s.audit(c, "host.upgrade_agent", "host", fmt.Sprint(id), detail("ok", r.Message))
	c.JSON(http.StatusOK, gin.H{"agent_version": req.Version, "message": r.Message})
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
	if spec.Mode == "sentinel" && !s.Lic.Snapshot().Feature(license.FeatureSentinel) || spec.Mode == "cluster" && !s.Lic.Snapshot().Feature(license.FeatureCluster) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含 "+spec.Mode+" 形态")
		return
	}
	if spec.TLS && !s.Lic.Snapshot().Feature(license.FeatureTLS) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含业务链路 TLS")
		return
	}
	u, op, err := s.Orch.CreateUnit(spec, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.create", "unit", fmt.Sprint(u.ID), gin.H{"name": u.Name, "mode": u.Mode, "memory_mb": u.MemoryMB, "replicas": u.Replicas, "modules": u.Modules, "operation": op.ID})
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

func (s *Server) upgradeUnit(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req orch.EngineUpgrade
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	op, err := s.Orch.UpgradeUnit(id, req, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.upgrade", "unit", fmt.Sprint(id), gin.H{"version": req.Version, "switchover": req.Switchover, "operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

func (s *Server) scaleUnit(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req orch.ScaleSpec
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	u, err := store.First[store.Unit](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	if u.Mode == store.ModeCluster && !s.Lic.Snapshot().Feature(license.FeatureCluster) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含集群扩缩容")
		return
	}
	op, err := s.Orch.ScaleUnit(id, req, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	audit := gin.H{"operation": op.ID}
	if req.Replicas != nil {
		audit["replicas"] = *req.Replicas
	}
	if req.Shards != nil {
		audit["shards"] = *req.Shards
	}
	s.audit(c, "unit.scale", "unit", fmt.Sprint(id), audit)
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

func (s *Server) sentinelOperation(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if !s.Lic.Snapshot().Feature(license.FeatureSentinel) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含哨兵管理")
		return
	}
	var req orch.SentinelChange
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	op, err := s.Orch.SentinelOp(id, req, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	audit := gin.H{"operation": op.ID, "operation_type": req.Operation, "master_name": req.MasterName}
	if req.Options != nil {
		masked := map[string]string{}
		for key, value := range req.Options {
			if key == "auth-pass" {
				masked[key] = "[redacted]"
			} else {
				masked[key] = value
			}
		}
		audit["options"] = masked
	}
	s.audit(c, "unit.sentinel", "unit", fmt.Sprint(id), audit)
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

// rollbackParams 把参数回滚到历史版本 :revision_id：以该版本的全量参数生成一个新版本（走与"应用参数"相同的操作流程）。
func (s *Server) rollbackParams(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		ParentRevisionID string `json:"parent_revision_id"`
		Note             string `json:"note"`
		Restart          bool   `json:"restart"`
		Switchover       bool   `json:"switchover"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	target := c.Param("revision_id")
	op, err := s.Orch.RollbackParams(id, target, orch.ParamChange{ParentRevisionID: req.ParentRevisionID, Note: req.Note, Restart: req.Restart, Switchover: req.Switchover}, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.params.rollback", "unit", fmt.Sprint(id), gin.H{"from": req.ParentRevisionID, "to": target, "note": req.Note, "restart": req.Restart, "operation": op.ID})
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

func (s *Server) unitInstance(c *gin.Context) (*store.Unit, *store.Instance, bool) {
	id, ok := parseID(c)
	if !ok {
		return nil, nil, false
	}
	u, err := store.First[store.Unit](s.Store, id)
	if err != nil {
		failErr(c, err)
		return nil, nil, false
	}
	iq := s.q().Instance
	if raw := c.Query("instance_id"); raw != "" {
		instanceID, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			fail(c, http.StatusBadRequest, "BAD_INSTANCE", "invalid instance_id")
			return nil, nil, false
		}
		in, err := iq.WithContext(c.Request.Context()).Where(iq.ID.Eq(uint(instanceID)), iq.UnitID.Eq(id)).First()
		if err != nil {
			failErr(c, err)
			return nil, nil, false
		}
		return u, in, true
	}
	ins, err := iq.WithContext(c.Request.Context()).Where(iq.UnitID.Eq(id), iq.Kind.Eq("server")).Order(iq.ID).Find()
	if err != nil || len(ins) == 0 {
		failErr(c, store.ErrNotFound)
		return nil, nil, false
	}
	for _, in := range ins {
		if in.Role == "master" || (in.Role == "" && in.Desired == "master") {
			return u, in, true
		}
	}
	return u, ins[0], true
}

func (s *Server) diagnose(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if _, err := store.First[store.Unit](s.Store, id); err != nil {
		failErr(c, err)
		return
	}
	kind := map[string]agentv1.Diagnose_Kind{"slowlog": agentv1.Diagnose_SLOWLOG, "latency": agentv1.Diagnose_LATENCY, "memory": agentv1.Diagnose_MEMORY, "bigkeys": agentv1.Diagnose_BIGKEYS, "clients": agentv1.Diagnose_CLIENTS, "rdb_analyze": agentv1.Diagnose_RDB_ANALYZE}[strings.ToLower(c.DefaultQuery("kind", "memory"))]
	if kind == agentv1.Diagnose_KIND_UNSPECIFIED {
		fail(c, http.StatusBadRequest, "BAD_DIAGNOSE", "kind must be slowlog, latency, memory, clients or rdb_analyze")
		return
	}
	if kind == agentv1.Diagnose_RDB_ANALYZE && !s.Lic.Snapshot().Feature(license.FeatureRDBAnalyze) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含 RDB 离线分析")
		return
	}
	sample, _ := strconv.Atoi(c.DefaultQuery("sample", "100"))
	if sample <= 0 || sample > 1000 {
		sample = 100
	}

	// An explicit instance_id preserves the single-instance response shape.
	// Without it, M7 aggregates the diagnostic across every data instance in
	// the deployment unit so slowlog/latency/client reports are not biased
	// toward whichever master happens to be listed first.
	iq := s.q().Instance
	var instances []*store.Instance
	if raw := c.Query("instance_id"); raw != "" {
		instanceID, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			fail(c, http.StatusBadRequest, "BAD_INSTANCE", "invalid instance_id")
			return
		}
		in, err := iq.WithContext(c.Request.Context()).Where(iq.ID.Eq(uint(instanceID)), iq.UnitID.Eq(id), iq.Kind.Eq("server")).First()
		if err != nil {
			failErr(c, err)
			return
		}
		instances = []*store.Instance{in}
	} else {
		var err error
		instances, err = iq.WithContext(c.Request.Context()).Where(iq.UnitID.Eq(id), iq.Kind.Eq("server")).Order(iq.ID).Find()
		if err != nil || len(instances) == 0 {
			if err == nil {
				err = store.ErrNotFound
			}
			failErr(c, err)
			return
		}
	}

	reports := make([]instanceReport, 0, len(instances))
	for _, in := range instances {
		r, err := s.GW.Send(c.Request.Context(), in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Diagnose{Diagnose: &agentv1.Diagnose{Port: in.Port, What: kind, Sample: uint32(sample)}}}, time.Minute)
		item := instanceReport{InstanceID: in.ID, HostID: in.HostID, Port: in.Port, Role: in.Role}
		if err != nil {
			item.Error = err.Error()
			reports = append(reports, item)
			continue
		}
		if r.Status != agentv1.CommandResult_OK {
			item.Error = r.Message
			if item.Error == "" {
				item.Error = r.ErrorCode
			}
			reports = append(reports, item)
			continue
		}
		if p, ok := decodeDiagnosePayload(r.Payload); ok {
			item.Report, item.Aggregate, item.Advice = p.Report, p.Aggregate, p.Advice
		} else {
			item.Report = gin.H{"raw": string(r.Payload)}
		}
		reports = append(reports, item)
	}

	s.audit(c, "unit.diagnose", "unit", fmt.Sprint(id), map[string]any{"kind": kind.String(), "instances": len(instances)})
	if c.Query("instance_id") != "" {
		c.JSON(http.StatusOK, gin.H{"kind": kind.String(), "sample": sample, "report": reports[0].Report,
			"aggregate": reports[0].Aggregate, "advice": reports[0].Advice, "error": reports[0].Error})
		return
	}
	c.JSON(http.StatusOK, gin.H{"kind": kind.String(), "sample": sample, "instances": reports})
}

// instanceReport 是诊断接口对单个实例的回传；Aggregate 与 Advice 仅在对应诊断类型下存在。
type instanceReport struct {
	InstanceID uint   `json:"instance_id"`
	HostID     uint   `json:"host_id"`
	Port       uint32 `json:"port"`
	Role       string `json:"role,omitempty"`
	Report     any    `json:"report,omitempty"`
	Aggregate  any    `json:"aggregate,omitempty"`
	Advice     any    `json:"advice,omitempty"`
	Error      string `json:"error,omitempty"`
}

// diagnosePayload 是 agent 诊断 JSON 负载的控制台侧视图；旧 agent 只有 report 字段。
type diagnosePayload struct {
	Report    any `json:"report"`
	Aggregate any `json:"aggregate,omitempty"`
	Advice    any `json:"advice,omitempty"`
}

// decodeDiagnosePayload 解析 agent 回传的诊断负载；非 JSON 时返回 false，调用方保留原始输出。
func decodeDiagnosePayload(raw []byte) (diagnosePayload, bool) {
	var p diagnosePayload
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return diagnosePayload{}, false
	}
	return p, true
}

func (s *Server) browseKeys(c *gin.Context) {
	u, in, ok := s.unitInstance(c)
	if !ok {
		return
	}
	reveal := c.Query("reveal") == "1" || strings.EqualFold(c.Query("reveal"), "true")
	if reveal {
		user, err := store.First[store.User](s.Store, c.GetUint("uid"))
		if err != nil || !user.KeyReveal {
			fail(c, http.StatusForbidden, "KEY_REVEAL_DENIED", "查看 Key 明文需要单独授权")
			return
		}
	}
	count, _ := strconv.Atoi(c.DefaultQuery("count", "100"))
	if count <= 0 || count > 200 {
		count = 100
	}
	r, err := s.GW.Send(c.Request.Context(), in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Keys{Keys: &agentv1.BrowseKeys{Port: in.Port, Pattern: c.DefaultQuery("pattern", "app:*"), Cursor: c.DefaultQuery("cursor", "0"), Count: uint32(count), Key: c.Query("key"), Reveal: reveal}}}, time.Minute)
	if err != nil {
		failErr(c, err)
		return
	}
	if r.Status != agentv1.CommandResult_OK {
		fail(c, http.StatusConflict, r.ErrorCode, r.Message)
		return
	}
	var payload any = gin.H{}
	if len(r.Payload) > 0 && json.Unmarshal(r.Payload, &payload) != nil {
		payload = gin.H{"raw": string(r.Payload)}
	}
	s.audit(c, "unit.keys.browse", "instance", fmt.Sprint(in.ID), map[string]any{"pattern": c.Query("pattern"), "reveal": reveal, "unit": u.ID})
	c.JSON(http.StatusOK, payload)
}

// cliBodyLimit：Web 命令行请求体上限（256 KiB 的参数加 JSON 外壳）。
const cliBodyLimit = 512 << 10

// execCommand：Web 命令行（CLAUDE.md「agent 与安全」中唯一的例外）。
// 中间件链：developer → writable → stepUp → requireTOTP；此处再查 CliExec 授权与 argv 白名单（与 agent 共用 webcli.Check）。
// 每条请求（包括被拒绝的请求）都写审计：操作人、部署单元、实例、命令名、参数个数、结果状态；参数值与回复内容不写审计、不写日志。
func (s *Server) execCommand(c *gin.Context) {
	u, in, ok := s.unitInstance(c)
	if !ok {
		return
	}
	if in.Kind != "server" {
		s.auditCLI(c, u, in, "", 0, "CLI_NOT_SERVER", "")
		fail(c, http.StatusBadRequest, "CLI_NOT_SERVER", "Web 命令行只能在数据实例上执行")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cliBodyLimit)
	var req struct {
		Argv []string `json:"argv"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.auditCLI(c, u, in, "", 0, webcli.CodeInvalid, "")
		fail(c, http.StatusBadRequest, webcli.CodeInvalid, "请求体需要是 {\"argv\": [...]} 形式的 JSON，且不超过 512 KiB")
		return
	}
	name, argc := webcli.CommandName(req.Argv), max(len(req.Argv)-1, 0)
	user, err := store.First[store.User](s.Store, c.GetUint("uid"))
	if err != nil {
		failErr(c, err)
		return
	}
	if !user.CliExec {
		s.auditCLI(c, u, in, name, argc, "CLI_EXEC_DENIED", "")
		fail(c, http.StatusForbidden, "CLI_EXEC_DENIED", "未获得命令行授权，请联系管理员")
		return
	}
	if _, err := webcli.Check(req.Argv); err != nil {
		code, status, msg := webcli.CodeInvalid, http.StatusBadRequest, "命令参数不合法"
		var we *webcli.Error
		if errors.As(err, &we) {
			code, msg = we.Code, we.Msg
			if we.Code == webcli.CodeDenied {
				status = http.StatusForbidden
			}
		}
		s.auditCLI(c, u, in, name, argc, code, "")
		fail(c, status, code, msg)
		return
	}
	send := s.sendCmd
	if send == nil {
		send = s.GW.Send
	}
	r, err := send(c.Request.Context(), in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_ExecCommand{ExecCommand: &agentv1.ExecCommand{Port: in.Port, Argv: req.Argv}}}, 15*time.Second)
	if err != nil {
		s.auditCLI(c, u, in, name, argc, "UNAVAILABLE", "")
		failErr(c, err)
		return
	}
	if r.Status != agentv1.CommandResult_OK {
		code := r.ErrorCode
		if code == "" {
			code = "FAILED"
		}
		s.auditCLI(c, u, in, name, argc, code, "")
		status := http.StatusConflict
		if r.ErrorCode == webcli.CodeDenied {
			status = http.StatusForbidden
		} else if r.ErrorCode == webcli.CodeInvalid {
			status = http.StatusBadRequest
		}
		fail(c, status, code, r.Message)
		return
	}
	var payload any = gin.H{}
	var replyType struct {
		Reply struct {
			Type string `json:"type"`
		} `json:"reply"`
	}
	if len(r.Payload) > 0 && json.Unmarshal(r.Payload, &payload) != nil {
		s.auditCLI(c, u, in, name, argc, "BAD_REPLY", "")
		fail(c, http.StatusBadGateway, "BAD_REPLY", "agent 返回的命令结果格式不正确")
		return
	}
	_ = json.Unmarshal(r.Payload, &replyType)
	s.auditCLI(c, u, in, name, argc, "OK", replyType.Reply.Type)
	c.JSON(http.StatusOK, payload)
}

// auditCLI 记录一条 Web 命令行审计。detail 只含命令名与参数个数，不含参数值或回复内容。
func (s *Server) auditCLI(c *gin.Context, u *store.Unit, in *store.Instance, name string, argc int, status, replyType string) {
	detail := map[string]any{"unit": u.ID, "instance_id": in.ID, "command": name, "argc": argc, "status": status}
	if replyType != "" {
		detail["reply_type"] = replyType
	}
	s.audit(c, "unit.cli.exec", "instance", fmt.Sprint(in.ID), detail)
}

func (s *Server) listBackups(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	bq := s.q().Backup
	rows, err := bq.WithContext(c.Request.Context()).Where(bq.UnitID.Eq(id)).Order(bq.CreatedAt.Desc()).Limit(100).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) getBackupPolicy(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	p, err := store.First[store.BackupPolicy](s.Store, id)
	if err == store.ErrNotFound {
		c.JSON(http.StatusOK, store.BackupPolicy{UnitID: id, Enabled: false, Cron: "02:00", KeepDays: 7})
		return
	}
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) saveBackupPolicy(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req store.BackupPolicy
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if !backupCronRe.MatchString(req.Cron) {
		fail(c, http.StatusBadRequest, "BAD_CRON", "执行时间必须是 HH:MM（控制台时区）")
		return
	}
	if req.KeepDays < 1 || req.KeepDays > 3650 {
		fail(c, http.StatusBadRequest, "BAD_RETENTION", "保留天数必须在 1 到 3650 之间")
		return
	}
	if req.Upload && !s.Lic.Snapshot().Feature(license.FeatureRemoteBkp) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含远端备份")
		return
	}
	req.UnitID = id
	req.UpdatedBy = c.GetString("user")
	req.UpdatedAt = time.Now()
	if err := s.q().BackupPolicy.WithContext(c.Request.Context()).Save(&req); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.backup_policy.save", "unit", fmt.Sprint(id), gin.H{"enabled": req.Enabled, "cron": req.Cron, "keep_days": req.KeepDays, "upload": req.Upload})
	c.JSON(http.StatusOK, req)
}

func (s *Server) createBackup(c *gin.Context) {
	var req struct {
		Upload     bool `json:"upload"`
		InstanceID uint `json:"instance_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		failErr(c, err)
		return
	}
	var in *store.Instance
	if req.InstanceID > 0 {
		id, ok := parseID(c)
		if !ok {
			return
		}
		iq := s.q().Instance
		var err error
		in, err = iq.WithContext(c.Request.Context()).Where(iq.ID.Eq(req.InstanceID), iq.UnitID.Eq(id), iq.Kind.Eq("server")).First()
		if err != nil {
			failErr(c, err)
			return
		}
	} else {
		var ok bool
		_, in, ok = s.unitInstance(c)
		if !ok {
			return
		}
	}
	if req.Upload && !s.Lic.Snapshot().Feature(license.FeatureRemoteBkp) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含远端备份")
		return
	}
	backupID := fmt.Sprintf("b-%d", time.Now().UnixNano())
	r, err := s.GW.Send(c.Request.Context(), in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Backup{Backup: &agentv1.Backup{Port: in.Port, BackupId: backupID, Upload: req.Upload}}}, 3*time.Minute)
	if err != nil {
		failErr(c, err)
		return
	}
	if r.Status != agentv1.CommandResult_OK {
		fail(c, http.StatusConflict, r.ErrorCode, r.Message)
		return
	}
	var meta struct {
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	_ = json.Unmarshal(r.Payload, &meta)
	if !req.Upload {
		row := &store.Backup{ID: backupID, UnitID: in.UnitID, InstanceID: in.ID, Location: "local", Size: meta.Size, SHA256: meta.SHA256, Status: "done", Trigger: "manual", CreatedBy: c.GetString("user"), CreatedAt: time.Now()}
		if err := s.q().Backup.WithContext(c.Request.Context()).Create(row); err != nil {
			failErr(c, err)
			return
		}
	}
	s.audit(c, "unit.backup.create", "backup", backupID, map[string]any{"unit_id": in.UnitID, "instance_id": in.ID, "upload": req.Upload, "size": meta.Size, "sha256": meta.SHA256})
	location := "local"
	if req.Upload {
		location = "console"
		if s.GW.BackupStore != nil {
			location = s.GW.BackupStore.Kind()
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"backup_id": backupID, "size": meta.Size, "sha256": meta.SHA256, "location": location})
}

func (s *Server) restoreBackup(c *gin.Context) {
	backupID := c.Param("backup_id")
	bq := s.q().Backup
	b, err := bq.WithContext(c.Request.Context()).Where(bq.ID.Eq(backupID)).First()
	if err != nil {
		failErr(c, store.ErrNotFound)
		return
	}
	var req struct {
		InstanceID uint `json:"instance_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		failErr(c, err)
		return
	}
	targetID := b.InstanceID
	if req.InstanceID > 0 {
		targetID = req.InstanceID
	}
	in, err := store.First[store.Instance](s.Store, targetID)
	if err != nil {
		failErr(c, err)
		return
	}
	if in.UnitID != b.UnitID {
		fail(c, http.StatusBadRequest, "BAD_INSTANCE", "目标实例不属于该部署单元")
		return
	}
	fromConsole := b.Location != "local"
	if !fromConsole {
		source, sourceErr := store.First[store.Instance](s.Store, b.InstanceID)
		if sourceErr != nil || source.HostID != in.HostID {
			fail(c, http.StatusConflict, "LOCAL_BACKUP_HOST", "本地备份只能恢复到同一主机；请先将备份上传到控制台")
			return
		}
	}
	r, err := s.GW.Send(c.Request.Context(), in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Restore{Restore: &agentv1.Restore{Port: in.Port, BackupId: backupID, FromConsole: fromConsole, Sha256: b.SHA256}}}, 3*time.Minute)
	if err != nil {
		failErr(c, err)
		return
	}
	if r.Status != agentv1.CommandResult_OK {
		fail(c, http.StatusConflict, r.ErrorCode, r.Message)
		return
	}
	s.audit(c, "unit.backup.restore", "backup", backupID, map[string]any{"unit_id": b.UnitID, "instance_id": b.InstanceID})
	c.JSON(http.StatusAccepted, gin.H{"backup_id": backupID, "restored": true})
}

// ---------- 在线迁移（M9） ----------

var migrationPatternRe = regexp.MustCompile(`^(?:\*|[A-Za-z0-9:_.\/-]+\*)$`)

type migrationRequest struct {
	SourceAddress  string `json:"source_address"`
	SourceUser     string `json:"source_user"`
	SourcePassword string `json:"source_password"`
	Pattern        string `json:"pattern"`
	Mode           string `json:"mode"` // full | full_incremental
	Batch          uint32 `json:"batch"`
	MaxPasses      uint32 `json:"max_passes"`
	SourceQuiesced bool   `json:"source_quiesced"`
	InstanceID     uint   `json:"instance_id"`
}

type migrationMeta struct {
	InstanceID     uint   `json:"instance_id"`
	SourceUser     string `json:"source_user"`
	Pattern        string `json:"pattern"`
	Mode           string `json:"mode"`
	Batch          uint32 `json:"batch"`
	MaxPasses      uint32 `json:"max_passes"`
	SourceQuiesced bool   `json:"source_quiesced"`
}

// mergeMigrationMeta keeps the durable request alongside the agent report.
// The report is replaced after every run, so without this merge a console
// restart would have no target instance or source ACL user with which to
// perform the explicit cutover.
func mergeMigrationMeta(progress string, meta migrationMeta) string {
	payload := map[string]any{}
	if strings.TrimSpace(progress) != "" {
		_ = json.Unmarshal([]byte(progress), &payload)
	}
	payload["instance_id"] = meta.InstanceID
	payload["source_user"] = meta.SourceUser
	payload["pattern"] = meta.Pattern
	payload["mode"] = meta.Mode
	payload["batch"] = meta.Batch
	payload["max_passes"] = meta.MaxPasses
	payload["source_quiesced"] = meta.SourceQuiesced
	return string(mustJSON(payload))
}

func (s *Server) listMigrations(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	mq := s.q().MigrationJob
	rows, err := mq.WithContext(c.Request.Context()).Where(mq.UnitID.Eq(id)).Order(mq.ID.Desc()).Limit(100).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) createMigration(c *gin.Context) {
	if !s.Lic.Snapshot().Feature(license.FeatureMigrate) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含在线数据迁移")
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req migrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if _, _, err := net.SplitHostPort(req.SourceAddress); err != nil {
		fail(c, http.StatusBadRequest, "BAD_SOURCE", "source_address 必须是 host:port")
		return
	}
	if strings.ContainsAny(req.SourceAddress, "\r\n\"'\\") || len(req.SourceAddress) > 253 {
		fail(c, http.StatusBadRequest, "BAD_SOURCE", "source_address 格式不安全")
		return
	}
	if req.Pattern == "" {
		req.Pattern = "*"
	}
	if !migrationPatternRe.MatchString(req.Pattern) {
		fail(c, http.StatusBadRequest, "BAD_PATTERN", "pattern 只能是 * 或字面前缀加 *")
		return
	}
	if req.Mode == "" {
		req.Mode = "full"
	}
	if req.Mode != "full" && req.Mode != "full_incremental" {
		fail(c, http.StatusBadRequest, "BAD_MODE", "mode 只能是 full 或 full_incremental")
		return
	}
	if req.Mode == "full_incremental" && req.Pattern != "*" {
		fail(c, http.StatusBadRequest, "BAD_PATTERN", "复制流增量会复制整个数据库，暂不支持前缀过滤；需要过滤时请选择全量模式")
		return
	}
	if len(req.SourceUser) > 64 || len(req.SourcePassword) > 512 {
		fail(c, http.StatusBadRequest, "BAD_AUTH", "源端账号或口令过长")
		return
	}
	if s.Box == nil && req.SourcePassword != "" {
		fail(c, http.StatusInternalServerError, "SECRETBOX_UNAVAILABLE", "控制台加密密钥不可用")
		return
	}
	var in *store.Instance
	if req.InstanceID > 0 {
		iq := s.q().Instance
		var err error
		in, err = iq.WithContext(c.Request.Context()).Where(iq.ID.Eq(req.InstanceID), iq.UnitID.Eq(id), iq.Kind.Eq("server")).First()
		if err != nil {
			failErr(c, err)
			return
		}
	} else {
		_, in, ok = s.unitInstance(c)
		if !ok {
			return
		}
	}
	sealed := ""
	if req.SourcePassword != "" {
		var err error
		sealed, err = s.Box.Seal(req.SourcePassword)
		if err != nil {
			failErr(c, err)
			return
		}
	}
	meta, _ := json.Marshal(map[string]any{"phase": "queued", "instance_id": in.ID, "source_user": req.SourceUser, "pattern": req.Pattern, "mode": req.Mode, "batch": req.Batch, "max_passes": req.MaxPasses, "source_quiesced": req.SourceQuiesced})
	job := &store.MigrationJob{UnitID: id, SourceAddr: req.SourceAddress, SourceAuth: sealed, Mode: req.Mode, Status: "queued", Progress: string(meta), CreatedBy: c.GetString("user"), CreatedAt: time.Now()}
	if err := s.q().MigrationJob.WithContext(c.Request.Context()).Create(job); err != nil {
		failErr(c, err)
		return
	}
	if err := s.Store.LockUnit(id, job.ID); err != nil {
		_, _ = s.q().MigrationJob.WithContext(c.Request.Context()).Where(s.q().MigrationJob.ID.Eq(job.ID)).UpdateSimple(s.q().MigrationJob.Status.Value("failed"), s.q().MigrationJob.Error.Value(err.Error()), s.q().MigrationJob.FinishedAt.Value(time.Now()))
		failErr(c, err)
		return
	}
	s.audit(c, "unit.migration.create", "migration", fmt.Sprint(job.ID), map[string]any{"unit_id": id, "instance_id": in.ID, "source_address": req.SourceAddress, "mode": req.Mode, "pattern": req.Pattern})
	s.startMigration(id, job.ID, in.HostID, in.Port)
	c.JSON(http.StatusAccepted, job)
}

func (s *Server) cutoverMigration(c *gin.Context) {
	if !s.Lic.Snapshot().Feature(license.FeatureMigrate) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含在线数据迁移")
		return
	}
	unitID, ok := parseID(c)
	if !ok {
		return
	}
	jobID, err := strconv.ParseUint(c.Param("migration_id"), 10, 64)
	if err != nil || jobID == 0 {
		fail(c, http.StatusBadRequest, "BAD_MIGRATION_ID", "invalid migration id")
		return
	}
	job, err := s.q().MigrationJob.WithContext(c.Request.Context()).Where(s.q().MigrationJob.ID.Eq(uint(jobID)), s.q().MigrationJob.UnitID.Eq(unitID)).First()
	if err != nil {
		failErr(c, err)
		return
	}
	if job.Status != "awaiting_cutover" {
		fail(c, http.StatusConflict, "MIGRATION_NOT_READY", "迁移任务当前不在等待切换状态")
		return
	}
	var req struct {
		SourceQuiesced bool `json:"source_quiesced"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !req.SourceQuiesced {
		fail(c, http.StatusBadRequest, "SOURCE_NOT_QUIESCED", "必须明确确认源端已停止写入")
		return
	}
	if owner, err := s.Store.UnitLockOwner(unitID); err != nil || owner != job.ID {
		fail(c, http.StatusConflict, "MIGRATION_LOCK_LOST", "迁移任务不再持有部署单元锁")
		return
	}
	var meta migrationMeta
	if err := json.Unmarshal([]byte(job.Progress), &meta); err != nil || meta.InstanceID == 0 {
		fail(c, http.StatusInternalServerError, "INVALID_MIGRATION_METADATA", "迁移任务元数据不可恢复")
		return
	}
	in, err := store.First[store.Instance](s.Store, meta.InstanceID)
	if err != nil || in.UnitID != unitID || in.Kind != "server" {
		fail(c, http.StatusConflict, "MIGRATION_TARGET_MISSING", "迁移目标实例不存在")
		return
	}
	password := ""
	if job.SourceAuth != "" {
		if s.Box == nil {
			fail(c, http.StatusInternalServerError, "SECRETBOX_UNAVAILABLE", "控制台加密密钥不可用")
			return
		}
		password, err = s.Box.Open(job.SourceAuth)
		if err != nil {
			fail(c, http.StatusInternalServerError, "SOURCE_AUTH_UNAVAILABLE", "源端凭据无法解密")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	r, err := s.GW.Send(ctx, in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Migrate{Migrate: &agentv1.Migrate{
		Port: in.Port, JobId: fmt.Sprint(job.ID), SourceAddress: job.SourceAddr, SourceUser: meta.SourceUser,
		SourcePassword: password, Mode: agentv1.Migrate_FULL_INCREMENTAL, SourceQuiesced: true, Finalize: true,
	}}}, 30*time.Minute)
	if err != nil {
		// Keep the task in awaiting_cutover so the operator can retry after
		// fixing the source quiescence or connectivity condition.
		_, _ = s.q().MigrationJob.WithContext(context.Background()).Where(s.q().MigrationJob.ID.Eq(job.ID)).UpdateSimple(s.q().MigrationJob.Error.Value(err.Error()))
		failErr(c, err)
		return
	}
	progress := string(r.Payload)
	if progress == "" {
		progress = job.Progress
	}
	meta.SourceQuiesced = true
	progress = mergeMigrationMeta(progress, meta)
	if r.Status != agentv1.CommandResult_OK {
		msg := r.Message
		if msg == "" {
			msg = "agent cutover failed"
		}
		_, _ = s.q().MigrationJob.WithContext(context.Background()).Where(s.q().MigrationJob.ID.Eq(job.ID)).UpdateSimple(s.q().MigrationJob.Progress.Value(progress), s.q().MigrationJob.Error.Value(msg))
		fail(c, http.StatusConflict, "MIGRATION_CUTOVER_FAILED", msg)
		return
	}
	var report struct {
		CutoverReady bool `json:"cutover_ready"`
	}
	if json.Unmarshal([]byte(progress), &report) != nil || !report.CutoverReady {
		_, _ = s.q().MigrationJob.WithContext(context.Background()).Where(s.q().MigrationJob.ID.Eq(job.ID)).UpdateSimple(s.q().MigrationJob.Progress.Value(progress), s.q().MigrationJob.Error.Value("agent did not confirm cutover readiness"))
		fail(c, http.StatusConflict, "MIGRATION_CUTOVER_FAILED", "agent 未确认切换完成")
		return
	}
	s.finishMigration(unitID, job.ID, "done", progress, "")
	s.audit(c, "unit.migration.cutover", "migration", fmt.Sprint(job.ID), map[string]any{"unit_id": unitID, "instance_id": in.ID})
	c.JSON(http.StatusAccepted, gin.H{"migration_id": job.ID, "cutover": true, "status": "done"})
}

func (s *Server) reshard(c *gin.Context) {
	if !s.Lic.Snapshot().Feature(license.FeatureReshard) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含在线迁槽")
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var spec orch.ReshardSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		failErr(c, err)
		return
	}
	op, err := s.Orch.Reshard(id, spec, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.reshard", "unit", fmt.Sprint(id), map[string]any{"source_instance_id": spec.SourceInstanceID, "target_instance_id": spec.TargetInstanceID, "slots": spec.Slots, "operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

// rebalance：dry_run 只返回基于已保存状态的计划，不下发指令；否则登记并后台执行再平衡。
func (s *Server) rebalance(c *gin.Context) {
	if !s.Lic.Snapshot().Feature(license.FeatureReshard) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含在线迁槽")
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		DryRun    bool   `json:"dry_run"`
		Batch     uint32 `json:"batch"`
		Tolerance uint32 `json:"tolerance"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if req.DryRun {
		plan, err := s.Orch.PreviewRebalance(id, req.Tolerance)
		if err != nil {
			failErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"dry_run": true, "plan": plan})
		return
	}
	op, err := s.Orch.Rebalance(id, orch.RebalanceSpec{Batch: req.Batch, Tolerance: req.Tolerance}, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "unit.rebalance", "unit", fmt.Sprint(id), map[string]any{"batch": req.Batch, "tolerance": req.Tolerance, "operation": op.ID})
	c.JSON(http.StatusAccepted, gin.H{"operation": op})
}

func (s *Server) runMigration(unitID, id, hostID uint, port uint32) {
	q := s.q().MigrationJob
	job, err := q.WithContext(context.Background()).Where(q.ID.Eq(id)).First()
	if err != nil {
		// 作业行读不到：没有进度可更新，但变更锁必须释放，否则该单元会一直被占用。
		if s.Log != nil {
			s.Log.Warn("migration job unreadable, releasing unit lock", "job", id, "unit", unitID, "err", err)
		}
		_ = s.Store.UnlockUnit(unitID, id)
		return
	}
	_, _ = q.WithContext(context.Background()).Where(q.ID.Eq(id)).Update(q.Status, "running")
	var meta migrationMeta
	if json.Unmarshal([]byte(job.Progress), &meta) != nil {
		s.finishMigration(unitID, id, "failed", job.Progress, "invalid migration metadata")
		return
	}
	password := ""
	if job.SourceAuth != "" {
		if s.Box == nil {
			s.finishMigration(unitID, id, "failed", job.Progress, "secretbox unavailable")
			return
		}
		password, err = s.Box.Open(job.SourceAuth)
		if err != nil {
			s.finishMigration(unitID, id, "failed", job.Progress, "source credentials cannot be decrypted")
			return
		}
	}
	mode := agentv1.Migrate_FULL
	if meta.Mode == "full_incremental" {
		mode = agentv1.Migrate_FULL_INCREMENTAL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	r, err := s.GW.Send(ctx, hostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Migrate{Migrate: &agentv1.Migrate{Port: port, JobId: fmt.Sprint(id), SourceAddress: job.SourceAddr, SourceUser: meta.SourceUser, SourcePassword: password, Pattern: meta.Pattern, Mode: mode, Batch: meta.Batch, MaxPasses: meta.MaxPasses, SourceQuiesced: meta.SourceQuiesced}}}, 30*time.Minute)
	if err != nil {
		s.finishMigration(unitID, id, "failed", job.Progress, err.Error())
		return
	}
	progress := string(r.Payload)
	if progress == "" {
		progress = job.Progress
	}
	progress = mergeMigrationMeta(progress, meta)
	status := "done"
	var summary struct {
		KeysFailed      uint64 `json:"keys_failed"`
		AwaitingCutover bool   `json:"awaiting_cutover"`
	}
	if json.Unmarshal([]byte(progress), &summary) == nil {
		if summary.AwaitingCutover {
			status = "awaiting_cutover"
		} else if summary.KeysFailed > 0 {
			status = "partial"
		}
	}
	if r.Status != agentv1.CommandResult_OK {
		status = "failed"
		if r.Message != "" {
			progress = string(mustJSON(map[string]any{"agent": json.RawMessage(progress), "error": r.Message}))
		}
	}
	s.finishMigration(unitID, id, status, progress, "")
}

func (s *Server) startMigration(unitID, id, hostID uint, port uint32) {
	if _, loaded := s.migrationRuns.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	go func() {
		defer s.migrationRuns.Delete(id)
		s.runMigration(unitID, id, hostID, port)
	}()
}

// RecoverMigrations is called by the HA leader after a console restart. The
// job request and the unit lock are durable; rerunning SCAN + DUMP/RESTORE is
// safe because RESTORE uses REPLACE, so an interrupted job can continue
// without exposing source credentials or values to the control plane.
func (s *Server) RecoverMigrations() {
	q := s.q().MigrationJob
	jobs, err := q.WithContext(context.Background()).Where(q.Status.In("queued", "running")).Find()
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("recover migrations", "err", err)
		}
		return
	}
	for _, job := range jobs {
		owner, err := s.Store.UnitLockOwner(job.UnitID)
		if err != nil || owner != job.ID {
			continue
		}
		var meta struct {
			InstanceID uint `json:"instance_id"`
		}
		if json.Unmarshal([]byte(job.Progress), &meta) != nil || meta.InstanceID == 0 {
			continue
		}
		in, err := store.First[store.Instance](s.Store, meta.InstanceID)
		if err != nil || in.UnitID != job.UnitID || in.Kind != "server" {
			continue
		}
		s.startMigration(job.UnitID, job.ID, in.HostID, in.Port)
	}
}

// finishMigration 记录作业结果并（非待切换状态时）释放变更锁。unitID 由调用方给出，
// 不再依赖读取作业行：读取失败不应让部署单元一直被锁住。
func (s *Server) finishMigration(unitID, id uint, status, progress, errMsg string) {
	q := s.q().MigrationJob
	if status == "awaiting_cutover" {
		_, _ = q.WithContext(context.Background()).Where(q.ID.Eq(id)).UpdateSimple(q.Status.Value(status), q.Progress.Value(progress), q.Error.Value(errMsg))
	} else {
		_, _ = q.WithContext(context.Background()).Where(q.ID.Eq(id)).UpdateSimple(q.Status.Value(status), q.Progress.Value(progress), q.Error.Value(errMsg), q.FinishedAt.Value(time.Now()))
	}
	if status != "awaiting_cutover" {
		_ = s.Store.UnlockUnit(unitID, id)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
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

func (s *Server) approveOperation(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	op, err := s.Orch.ApproveOperation(id, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "operation.approve", "operation", fmt.Sprint(id), gin.H{"unit_id": op.UnitID, "requester": op.CreatedBy})
	c.JSON(http.StatusAccepted, gin.H{"operation": s.opView(*op)})
}

func (s *Server) resumeOperation(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	op, err := s.Orch.ResumeOperation(id, c.GetString("user"))
	if err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "operation.resume", "operation", fmt.Sprint(id), gin.H{"unit_id": op.UnitID, "requester": op.CreatedBy})
	c.JSON(http.StatusAccepted, gin.H{"operation": s.opView(*op)})
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
