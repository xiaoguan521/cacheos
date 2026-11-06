package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiaoguan521/cacheos/internal/console/secpolicy"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/pkg/crypto"
)

// 账号安全（等保 2.0 三级）：口令复杂度、有效期、历史、账号锁定、会话超时；用户停用、改角色、重置、删除。

func (s *Server) getPolicy(c *gin.Context) { c.JSON(http.StatusOK, s.policy()) }

func (s *Server) putPolicy(c *gin.Context) {
	var p secpolicy.Policy
	if err := c.ShouldBindJSON(&p); err != nil {
		failErr(c, err)
		return
	}
	if err := p.Validate(); err != nil {
		fail(c, http.StatusBadRequest, "BAD_POLICY", err.Error())
		return
	}
	b, _ := json.Marshal(p)
	if err := s.Store.SetKV("security_policy", string(b)); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "settings.security_policy", "settings", "security_policy", p)
	c.JSON(http.StatusOK, p)
}

func (s *Server) getApprovalPolicy(c *gin.Context) {
	required := s.Orch != nil && s.Orch.RequireApproval
	c.JSON(http.StatusOK, gin.H{"required": required})
}

func (s *Server) putApprovalPolicy(c *gin.Context) {
	var req struct {
		Required bool `json:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	if err := s.Store.SetKV("approval_required", map[bool]string{true: "1", false: "0"}[req.Required]); err != nil {
		failErr(c, err)
		return
	}
	if s.Orch != nil {
		s.Orch.RequireApproval = req.Required
	}
	s.audit(c, "settings.approval_policy", "settings", "approval_required", req)
	c.JSON(http.StatusOK, gin.H{"required": req.Required})
}

// changePassword：本地账号修改自己的口令（首次登录或过期时强制）。
func (s *Server) changePassword(c *gin.Context) {
	var req struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	uq := s.q().User
	u, err := uq.WithContext(c.Request.Context()).Where(uq.ID.Eq(c.GetUint("uid"))).First()
	if err != nil {
		failErr(c, store.ErrNotFound)
		return
	}
	if !isLocal(u) {
		fail(c, http.StatusBadRequest, "EXTERNAL_ACCOUNT", "该账号由 "+u.Source+" 管理，请在身份源修改口令")
		return
	}
	if !crypto.CheckPassword(u.PasswordHash, req.Old) {
		s.audit(c, "auth.password_change", "user", u.Username, map[string]string{"result": "bad old password"})
		fail(c, http.StatusBadRequest, "BAD_PASSWORD", "原口令错误")
		return
	}
	pol := s.policy()
	if err := pol.CheckPassword(u.Username, req.New); err != nil {
		fail(c, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	if secpolicy.InHistory(req.New, u.PasswordHash, u.PasswordHistory) {
		fail(c, http.StatusBadRequest, "PASSWORD_REUSED", "不能与最近使用过的口令相同")
		return
	}
	h, err := crypto.HashPassword(req.New)
	if err != nil {
		failErr(c, err)
		return
	}
	now := time.Now()
	if _, err := uq.WithContext(c.Request.Context()).Where(uq.ID.Eq(u.ID)).UpdateSimple(uq.PasswordHash.Value(h), uq.PasswordChangedAt.Value(now),
		uq.MustChangePassword.Value(false), uq.PasswordHistory.Value(secpolicy.PushHistory(u.PasswordHistory, u.PasswordHash, pol.History))); err != nil {
		failErr(c, err)
		return
	}
	u.MustChangePassword, u.PasswordChangedAt, u.PasswordHash = false, &now, h
	s.audit(c, "auth.password_change", "user", u.Username, nil)
	_ = s.issue(c, u) // 解除“必须改口令”限制
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// updateUser：改角色、停用 / 启用、解锁、重置口令（重置后首次登录必须修改）。
func (s *Server) updateUser(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req struct {
		Role          *string `json:"role"`
		Disabled      *bool   `json:"disabled"`
		KeyReveal     *bool   `json:"key_reveal"`
		Unlock        bool    `json:"unlock"`
		ResetPassword string  `json:"reset_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	uq := s.q().User
	ctx := c.Request.Context()
	u, err := uq.WithContext(ctx).Where(uq.ID.Eq(id)).First()
	if err != nil {
		failErr(c, store.ErrNotFound)
		return
	}
	self := u.ID == c.GetUint("uid")
	if self && ((req.Role != nil && *req.Role != u.Role) || (req.Disabled != nil && *req.Disabled)) {
		fail(c, http.StatusBadRequest, "SELF", "不能修改自己的角色或停用自己")
		return
	}
	if req.Role != nil {
		if _, ok := rank[*req.Role]; !ok {
			fail(c, http.StatusBadRequest, "BAD_ROLE", "unknown role")
			return
		}
		if u.Role == store.RoleAdmin && *req.Role != store.RoleAdmin && s.adminCount() <= 1 {
			fail(c, http.StatusBadRequest, "LAST_ADMIN", "至少保留一个管理员")
			return
		}
		u.Role = *req.Role
	}
	if req.Disabled != nil {
		if *req.Disabled && u.Role == store.RoleAdmin && s.adminCount() <= 1 {
			fail(c, http.StatusBadRequest, "LAST_ADMIN", "至少保留一个启用的管理员")
			return
		}
		u.Disabled = *req.Disabled
	}
	if req.KeyReveal != nil {
		if u.Role != store.RoleDeveloper && u.Role != store.RoleAdmin {
			fail(c, http.StatusBadRequest, "KEY_REVEAL_ROLE", "只有开发者或管理员可以获得 Key 明文查看授权")
			return
		}
		u.KeyReveal = *req.KeyReveal
	}
	if req.Unlock {
		u.LockedUntil, u.FailedLogins = nil, 0
	}
	if req.ResetPassword != "" {
		if !isLocal(u) {
			fail(c, http.StatusBadRequest, "EXTERNAL_ACCOUNT", "外部身份账号不能重置口令")
			return
		}
		if err := s.policy().CheckPassword(u.Username, req.ResetPassword); err != nil {
			fail(c, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
			return
		}
		h, err := crypto.HashPassword(req.ResetPassword)
		if err != nil {
			failErr(c, err)
			return
		}
		u.PasswordHistory = secpolicy.PushHistory(u.PasswordHistory, u.PasswordHash, s.policy().History)
		u.PasswordHash, u.MustChangePassword = h, true
	}
	if err := uq.WithContext(ctx).Save(u); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "user.update", "user", u.Username, map[string]any{"role": req.Role, "disabled": req.Disabled, "key_reveal": req.KeyReveal, "unlock": req.Unlock, "password_reset": req.ResetPassword != ""})
	c.JSON(http.StatusOK, u)
}

func (s *Server) deleteUser(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if id == c.GetUint("uid") {
		fail(c, http.StatusBadRequest, "SELF", "不能删除自己")
		return
	}
	uq := s.q().User
	u, err := uq.WithContext(c.Request.Context()).Where(uq.ID.Eq(id)).First()
	if err != nil {
		failErr(c, store.ErrNotFound)
		return
	}
	if u.Role == store.RoleAdmin && s.adminCount() <= 1 {
		fail(c, http.StatusBadRequest, "LAST_ADMIN", "至少保留一个管理员")
		return
	}
	if _, err := uq.WithContext(c.Request.Context()).Where(uq.ID.Eq(id)).Delete(); err != nil {
		failErr(c, err)
		return
	}
	// 令牌随用户失效（tokenAuth 会检查用户存在），这里一并撤销以便列表清晰
	tq := s.q().APIToken
	_, _ = tq.WithContext(c.Request.Context()).Where(tq.Username.Eq(u.Username)).Update(tq.Revoked, true)
	s.audit(c, "user.delete", "user", u.Username, nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) adminCount() int64 {
	uq := s.q().User
	n, _ := uq.WithContext(bg).Where(uq.Role.Eq(store.RoleAdmin), uq.Disabled.Is(false)).Count()
	return n
}
