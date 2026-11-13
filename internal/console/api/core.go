package api

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"gorm.io/gen/field"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/pkg/crypto"
)

// bg 用于不依附单个请求的统计查询。
var bg = context.Background()

func (s *Server) q() *query.Query { return query.Use(s.Store.DB) }

// ---------- 初始化与登录 ----------

type licenseView struct {
	State          string    `json:"state"`
	Reason         string    `json:"reason"`
	Writable       bool      `json:"writable"`
	LicenseID      string    `json:"license_id,omitempty"`
	Customer       string    `json:"customer,omitempty"`
	Edition        string    `json:"edition,omitempty"`
	Features       []string  `json:"features,omitempty"`
	Hosts          int       `json:"hosts_limit"`
	Instances      int       `json:"instances_limit"`
	MemoryGB       int       `json:"memory_gb_limit"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	MaintUntil     time.Time `json:"maintenance_until,omitempty"`
	DaysLeft       int       `json:"days_left"`
	InstallationID string    `json:"installation_id"`
	HostsUsed      int64     `json:"hosts_used"`
	InstancesUsed  int64     `json:"instances_used"`
	MemoryMBUsed   uint64    `json:"memory_mb_used"`
	CRL            any       `json:"crl"` // 当前吊销列表摘要，没有时为 null
}

func (s *Server) licView() licenseView {
	snap := s.Lic.Snapshot()
	v := licenseView{State: string(snap.State), Reason: snap.Reason, Writable: snap.Writable(), InstallationID: s.Lic.InstallationID()}
	if p := snap.Payload; p != nil {
		v.LicenseID, v.Customer, v.Edition = p.LicenseID, p.Customer.Name, string(p.Edition)
		for _, f := range p.Features {
			v.Features = append(v.Features, string(f))
		}
		v.Hosts, v.Instances, v.MemoryGB, v.ExpiresAt, v.MaintUntil = p.Limits.Hosts, p.Limits.Instances, p.Limits.MemoryGB, p.ExpiresAt, p.MaintenanceUntil
		v.DaysLeft = int(time.Until(p.ExpiresAt).Hours() / 24)
	}
	if info := s.Lic.CRLInfo(); info != nil {
		v.CRL = info
	}
	q := s.q()
	v.HostsUsed, _ = q.Host.WithContext(bg).Where(q.Host.Status.Neq("revoked")).Count()
	v.InstancesUsed, _ = q.Instance.WithContext(bg).Count()
	units, _ := q.Unit.WithContext(bg).Find()
	for _, u := range units {
		v.MemoryMBUsed += uint64(u.MemoryMB) * uint64(u.Shards*(1+u.Replicas))
	}
	return v
}

func (s *Server) userCount() int64 {
	n, _ := s.q().User.WithContext(bg).Count()
	return n
}

func (s *Server) setupStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"initialized": s.userCount() > 0, "license": s.licView(), "oidc": s.oidcEnabled()})
}

type credentials struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	OTP      string `json:"otp,omitempty"`
}

func (s *Server) totpKey(uid uint) string        { return fmt.Sprintf("totp.secret.%d", uid) }
func (s *Server) totpEnabledKey(uid uint) string { return fmt.Sprintf("totp.enabled.%d", uid) }

func (s *Server) saveTOTP(uid uint, secret string) error {
	stored := secret
	if s.Box != nil {
		var err error
		stored, err = s.Box.Seal(secret)
		if err != nil {
			return err
		}
	}
	if err := s.Store.SetKV(s.totpKey(uid), stored); err != nil {
		return err
	}
	return s.Store.SetKV(s.totpEnabledKey(uid), "1")
}

func (s *Server) loadTOTP(uid uint) (string, bool) {
	stored, err := s.Store.GetKV(s.totpKey(uid))
	if err != nil || stored == "" {
		return "", false
	}
	if s.Box != nil && strings.HasPrefix(stored, "v1:") {
		stored, err = s.Box.Open(stored)
		if err != nil {
			return "", false
		}
	}
	enabled, _ := s.Store.GetKV(s.totpEnabledKey(uid))
	return stored, enabled == "1"
}

func newTOTP(username string) (string, string, error) {
	// Explicit entropy keeps provisioning secrets independent of the default
	// library size and makes the key size visible to security review.
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "CacheOS", AccountName: username, Secret: buf})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func (s *Server) setup(c *gin.Context) {
	if s.userCount() > 0 {
		fail(c, http.StatusConflict, "ALREADY_INITIALIZED", "already initialized")
		return
	}
	var req credentials
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if !usernameRe.MatchString(req.Username) {
		fail(c, http.StatusBadRequest, "BAD_USERNAME", "用户名只能包含小写字母、数字和连字符，2–63 位")
		return
	}
	if err := s.policy().CheckPassword(req.Username, req.Password); err != nil {
		fail(c, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	h, err := crypto.HashPassword(req.Password)
	if err != nil {
		failErr(c, err)
		return
	}
	now := time.Now()
	u := &store.User{Username: req.Username, PasswordHash: h, Role: store.RoleAdmin, Source: "local", PasswordChangedAt: &now}
	q, ctx := s.q(), c.Request.Context()
	if err := q.User.WithContext(ctx).Create(u); err != nil {
		failErr(c, err)
		return
	}
	secret, uri, err := newTOTP(u.Username)
	if err != nil {
		_, _ = q.User.WithContext(ctx).Where(q.User.ID.Eq(u.ID)).Delete(u)
		fail(c, http.StatusInternalServerError, "TOTP_SETUP_FAILED", "无法初始化双因素认证")
		return
	}
	if err := s.saveTOTP(u.ID, secret); err != nil {
		_, _ = q.User.WithContext(ctx).Where(q.User.ID.Eq(u.ID)).Delete(u)
		fail(c, http.StatusInternalServerError, "TOTP_SETUP_FAILED", "无法初始化双因素认证")
		return
	}
	c.Set("user", u.Username)
	s.audit(c, "system.setup", "user", u.Username, nil)
	_ = s.issue(c, u)
	c.JSON(http.StatusOK, gin.H{"username": u.Username, "role": u.Role, "totp_required": true, "totp_secret": secret, "totp_uri": uri})
}

func (s *Server) login(c *gin.Context) {
	ip := c.ClientIP()
	v, _ := s.fails.LoadOrStore(ip, &loginFail{})
	lf := v.(*loginFail)
	lf.mu.Lock()
	locked := time.Now().Before(lf.until)
	lf.mu.Unlock()
	if locked {
		fail(c, http.StatusTooManyRequests, "LOCKED", "登录失败次数过多，请 15 分钟后再试")
		return
	}
	var req credentials
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	uq, ctx := s.q().User, c.Request.Context()
	pol := s.policy()
	u, err := uq.WithContext(ctx).Where(uq.Username.Eq(req.Username)).First()
	if err == nil && u.LockedUntil != nil && time.Now().Before(*u.LockedUntil) {
		c.Set("user", req.Username)
		s.auditEntry("auth.login", auditEntry(c, "auth.login", "user", req.Username, "locked"))
		fail(c, http.StatusTooManyRequests, "ACCOUNT_LOCKED", fmt.Sprintf("账号已锁定，请于 %s 后再试或联系管理员解锁", u.LockedUntil.Format("15:04")))
		return
	}
	var ok bool
	switch {
	case err == nil && isLocal(u):
		ok = crypto.CheckPassword(u.PasswordHash, req.Password)
	case s.ldapEnabled() && (err != nil || u.Source == "ldap"):
		// 本地没有此用户或用户来自 LDAP：交给 LDAP 认证（首次登录自动创建，角色按映射）
		u, ok = s.ldapLogin(c, req.Username, req.Password)
		err = nil
	}
	if err == nil && ok && u.Disabled {
		c.Set("user", u.Username)
		s.auditEntry("auth.login", auditEntry(c, "auth.login", "user", u.Username, "disabled"))
		fail(c, http.StatusForbidden, "ACCOUNT_DISABLED", "账号已停用")
		return
	}
	if !ok {
		lf.mu.Lock()
		if lf.n++; lf.n >= 5*2 { // 单个来源 IP 的失败上限（防撞库），账号级锁定见下
			lf.n, lf.until = 0, time.Now().Add(15*time.Minute)
		}
		lf.mu.Unlock()
		if err == nil && u != nil && u.ID != 0 {
			n := u.FailedLogins + 1
			set := []field.AssignExpr{uq.FailedLogins.Value(n)}
			if n >= pol.LockThreshold {
				set = []field.AssignExpr{uq.FailedLogins.Value(0), uq.LockedUntil.Value(time.Now().Add(time.Duration(pol.LockMinutes) * time.Minute))}
			}
			if _, err := uq.WithContext(ctx).Where(uq.ID.Eq(u.ID)).UpdateSimple(set...); err != nil {
				s.logger().Error("login: persist failed-login counter failed", "user_id", u.ID, "err", err)
			}
		}
		c.Set("user", req.Username)
		s.auditEntry("auth.login", auditEntry(c, "auth.login", "user", req.Username, "failure"))
		fail(c, http.StatusUnauthorized, "BAD_CREDENTIALS", "用户名或密码错误")
		return
	}
	if secret, enabled := s.loadTOTP(u.ID); enabled && !totp.Validate(req.OTP, secret) {
		c.Set("user", req.Username)
		s.auditEntry("auth.login", auditEntry(c, "auth.login", "user", req.Username, "totp_failure"))
		code := "TOTP_REQUIRED"
		if req.OTP != "" {
			code = "TOTP_INVALID"
		}
		fail(c, http.StatusUnauthorized, code, "请输入有效的身份验证器验证码")
		return
	}
	lf.mu.Lock()
	lf.n = 0
	lf.mu.Unlock()
	if _, err := uq.WithContext(ctx).Where(uq.ID.Eq(u.ID)).UpdateSimple(uq.LastLoginAt.Value(time.Now()), uq.FailedLogins.Value(0), uq.LockedUntil.Null()); err != nil {
		s.logger().Error("login: reset failed-login counter failed", "user_id", u.ID, "err", err)
	}
	c.Set("user", u.Username)
	s.audit(c, "auth.login", "user", u.Username, map[string]string{"source": u.Source})
	if err := s.issue(c, u); err != nil {
		failErr(c, err)
		return
	}
	mustChange := u.MustChangePassword || (isLocal(u) && pol.Expired(u.PasswordChangedAt, u.CreatedAt, time.Now()))
	c.JSON(http.StatusOK, gin.H{"username": u.Username, "role": u.Role, "must_change_password": mustChange})
}

// setupTOTP 为已登录账号重新生成身份验证器密钥；密钥只在本次响应中返回。
func (s *Server) setupTOTP(c *gin.Context) {
	u, err := store.First[store.User](s.Store, c.GetUint("uid"))
	if err != nil {
		failErr(c, err)
		return
	}
	secret, uri, err := newTOTP(u.Username)
	if err != nil {
		failErr(c, err)
		return
	}
	if err := s.saveTOTP(u.ID, secret); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "auth.totp_setup", "user", u.Username, nil)
	c.JSON(http.StatusOK, gin.H{"enabled": true, "secret": secret, "uri": uri})
}

func (s *Server) totpStatus(c *gin.Context) {
	_, enabled := s.loadTOTP(c.GetUint("uid"))
	c.JSON(http.StatusOK, gin.H{"enabled": enabled})
}

// stepUp protects high-impact writes with a fresh authenticator code.  The
// code is intentionally supplied in a header so it never becomes part of an
// operation request, audit detail, or database record.
func (s *Server) stepUp(c *gin.Context) {
	secret, enabled := s.loadTOTP(c.GetUint("uid"))
	if !enabled {
		c.Next()
		return
	}
	code := strings.TrimSpace(c.GetHeader("X-TOTP-Code"))
	if code == "" {
		fail(c, http.StatusUnauthorized, "TOTP_STEP_UP_REQUIRED", "高风险操作需要新的身份验证器验证码")
		return
	}
	if !totp.Validate(code, secret) {
		fail(c, http.StatusUnauthorized, "TOTP_STEP_UP_INVALID", "身份验证器验证码无效或已过期")
		return
	}
	c.Next()
}

func (s *Server) logout(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteStrictMode})
	c.Status(http.StatusNoContent)
}

// requireTOTP：Web 命令行每次请求都必须有已启用的 TOTP。stepUp 只在启用后校验验证码，这里补上未启用时的拒绝（fail-closed）。
func (s *Server) requireTOTP(c *gin.Context) {
	if _, enabled := s.loadTOTP(c.GetUint("uid")); !enabled {
		fail(c, http.StatusForbidden, "TOTP_REQUIRED", "Web 命令行需要先在「系统」中启用身份验证器（TOTP）")
		return
	}
	c.Next()
}

func (s *Server) me(c *gin.Context) {
	cliExec := false
	if u, err := store.First[store.User](s.Store, c.GetUint("uid")); err == nil {
		cliExec = u.CliExec
	}
	c.JSON(http.StatusOK, gin.H{"username": c.GetString("user"), "role": c.GetString("role"), "must_change_password": c.GetBool("pc"), "cli_exec": cliExec})
}

// ---------- 授权 ----------

func (s *Server) licenseGet(c *gin.Context) { c.JSON(http.StatusOK, s.licView()) }

func (s *Server) licenseRequest(c *gin.Context) {
	c.Header("Content-Disposition", `attachment; filename="activation-request.json"`)
	c.JSON(http.StatusOK, s.Lic.ActivationRequest())
}

func (s *Server) licenseImport(c *gin.Context) {
	var req struct {
		Raw string `json:"raw" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	snap, err := s.Lic.Import(req.Raw, c.GetString("user"))
	if err != nil {
		fail(c, http.StatusBadRequest, "LICENSE_REJECTED", err.Error())
		return
	}
	id := ""
	if snap.Payload != nil {
		id = snap.Payload.LicenseID
	}
	detail := map[string]any{"state": string(snap.State)}
	if info := s.Lic.CRLInfo(); info != nil {
		detail["crl_serial"] = info.Serial
	}
	s.audit(c, "license.import", "license", id, detail)
	go s.pushLicense()
	c.JSON(http.StatusOK, s.licView())
}

// licenseCRL 单独导入吊销列表（授权过期 / 无效时仍可用）。
func (s *Server) licenseCRL(c *gin.Context) {
	var req struct {
		Raw string `json:"raw" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	crl, err := s.Lic.ImportCRL(req.Raw)
	if err != nil {
		fail(c, http.StatusBadRequest, "CRL_REJECTED", err.Error())
		return
	}
	s.audit(c, "license.crl_import", "crl", fmt.Sprint(crl.Serial), map[string]any{"revoked": len(crl.RevokedLicenses), "revoked_kids": crl.RevokedKIDs})
	go s.pushLicense()
	c.JSON(http.StatusOK, s.licView())
}

// ---------- 通用 ----------

func parseID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		fail(c, http.StatusBadRequest, "BAD_ID", "invalid id")
		return 0, false
	}
	return uint(id), true
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// ---------- 审计 ----------

func (s *Server) listAudit(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	a := s.q().AuditLog
	rows, err := a.WithContext(c.Request.Context()).Order(a.ID.Desc()).Limit(limit).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) verifyAudit(c *gin.Context) {
	bad, err := s.Audit.Verify()
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"intact": bad == 0, "first_bad_id": bad})
}

func (s *Server) exportAudit(c *gin.Context) {
	a := s.q().AuditLog
	rows, err := a.WithContext(c.Request.Context()).Order(a.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="audit.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"id", "ts", "username", "ip", "action", "object_type", "object_id", "result", "detail", "hash"})
	for _, r := range rows {
		_ = w.Write([]string{fmt.Sprint(r.ID), r.TS.Format(time.RFC3339), r.Username, r.IP, r.Action, r.ObjectType, r.ObjectID, r.Result, r.Detail, r.Hash})
	}
	w.Flush()
	s.audit(c, "audit.export", "audit", "", nil)
}

// ---------- 用户 ----------

func (s *Server) listUsers(c *gin.Context) {
	uq := s.q().User
	us, err := uq.WithContext(c.Request.Context()).Order(uq.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	c.JSON(http.StatusOK, us)
}

func (s *Server) createUser(c *gin.Context) {
	var req struct {
		credentials
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if _, ok := rank[req.Role]; !ok {
		fail(c, http.StatusBadRequest, "BAD_ROLE", "unknown role")
		return
	}
	if !usernameRe.MatchString(req.Username) {
		fail(c, http.StatusBadRequest, "BAD_USERNAME", "用户名只能包含小写字母、数字和连字符")
		return
	}
	if err := s.policy().CheckPassword(req.Username, req.Password); err != nil {
		fail(c, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	h, err := crypto.HashPassword(req.Password)
	if err != nil {
		failErr(c, err)
		return
	}
	// 管理员设置的初始口令：首次登录必须修改
	u := &store.User{Username: req.Username, PasswordHash: h, Role: req.Role, Source: "local", MustChangePassword: true}
	if err := s.q().User.WithContext(c.Request.Context()).Create(u); err != nil {
		fail(c, http.StatusConflict, "DUPLICATE", "用户已存在")
		return
	}
	s.audit(c, "user.create", "user", u.Username, map[string]string{"role": u.Role})
	c.JSON(http.StatusOK, u)
}

// pushLicense 把新 License 推送给所有在线主机（agent 侧独立校验）。
func (s *Server) pushLicense() {
	hq := s.q().Host
	hs, _ := hq.WithContext(bg).Where(hq.Status.Eq("online")).Find()
	for _, h := range hs {
		s.GW.PushLicense(h.ID)
	}
}

func auditEntry(c *gin.Context, action, objType, objID, result string) auditE {
	return auditE{Username: c.GetString("user"), IP: c.ClientIP(), Action: action, ObjectType: objType, ObjectID: objID, Result: result}
}
