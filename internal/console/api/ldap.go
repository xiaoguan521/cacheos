package api

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-ldap/ldap/v3"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// LDAP 登录（规格书 7.1 M11，授权功能 sso）。只允许 ldaps:// 或 ldap:// + StartTLS，绝不明文传输口令；
// 用户首次登录自动创建（来源 ldap），角色按组映射，每次登录刷新；本地口令对 LDAP 账号无效。

// LDAPConfig 是 LDAP 设置（BindPassword 只写不读，加密保存）。
type LDAPConfig struct {
	Enabled        bool          `json:"enabled"`
	URL            string        `json:"url"` // ldaps://ad.example.com:636 或 ldap://...:389（强制 StartTLS）
	BindDN         string        `json:"bind_dn"`
	BindPassword   string        `json:"bind_password,omitempty"`
	BindPassEnc    string        `json:"bind_password_enc,omitempty"`
	BaseDN         string        `json:"base_dn"`
	UserFilter     string        `json:"user_filter"` // 例如 (&(objectClass=person)(uid=%s))，%s 为转义后的用户名
	DefaultRole    string        `json:"default_role"`
	RoleMap        []LDAPRoleMap `json:"role_map"` // 组 DN → 角色（取最高）
	SkipTLSVerify  bool          `json:"skip_tls_verify"`
	HasBindPass    bool          `json:"has_bind_password"`
	TimeoutSeconds int           `json:"timeout_seconds"`
}

// LDAPRoleMap 把一个组映射到角色。
type LDAPRoleMap struct {
	Group string `json:"group"`
	Role  string `json:"role"`
}

func (s *Server) ldapConfig() LDAPConfig {
	var c LDAPConfig
	if v, err := s.Store.GetKV("ldap"); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	if c.UserFilter == "" {
		c.UserFilter = "(&(objectClass=person)(uid=%s))"
	}
	if c.DefaultRole == "" {
		c.DefaultRole = store.RoleViewer
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 10
	}
	return c
}

func (s *Server) ldapEnabled() bool {
	return s.ldapConfig().Enabled && s.Lic.Snapshot().Feature(license.FeatureSSO)
}

func (cfg *LDAPConfig) validate() error {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "ldaps" && u.Scheme != "ldap") || u.Hostname() == "" {
		return errors.New("地址格式为 ldaps://主机:636 或 ldap://主机:389（强制 StartTLS）")
	}
	if cfg.BaseDN == "" || !strings.Contains(cfg.UserFilter, "%s") || strings.Count(cfg.UserFilter, "%s") != 1 || strings.Contains(strings.ReplaceAll(cfg.UserFilter, "%s", ""), "%") {
		return errors.New("请填写 Base DN，用户过滤器需包含且只包含一个 %s")
	}
	if _, ok := rank[cfg.DefaultRole]; !ok {
		return errors.New("默认角色无效")
	}
	for _, m := range cfg.RoleMap {
		if _, ok := rank[m.Role]; !ok || m.Group == "" {
			return errors.New("组映射无效")
		}
	}
	return nil
}

func (s *Server) ldapDial(cfg LDAPConfig) (*ldap.Conn, error) {
	u, _ := url.Parse(cfg.URL)
	tc := &tls.Config{ServerName: u.Hostname(), InsecureSkipVerify: cfg.SkipTLSVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // 管理员显式选择（内网自签证书）
	conn, err := ldap.DialURL(cfg.URL, ldap.DialWithTLSConfig(tc))
	if err != nil {
		return nil, err
	}
	conn.SetTimeout(time.Duration(cfg.TimeoutSeconds) * time.Second)
	if u.Scheme == "ldap" {
		if err := conn.StartTLS(tc); err != nil {
			conn.Close()
			return nil, fmt.Errorf("StartTLS 失败（拒绝明文传输口令）：%w", err)
		}
	}
	return conn, nil
}

// ldapAuth 认证并返回用户 DN 与映射后的角色。
func (s *Server) ldapAuth(cfg LDAPConfig, username, password string) (string, string, error) {
	if password == "" {
		return "", "", errors.New("empty password") // 防止匿名 / 未认证绑定被当作成功
	}
	conn, err := s.ldapDial(cfg)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	if cfg.BindDN != "" {
		pass := ""
		if cfg.BindPassEnc != "" {
			if pass, err = s.Certs.Box.Open(cfg.BindPassEnc); err != nil {
				return "", "", err
			}
		}
		if err := conn.Bind(cfg.BindDN, pass); err != nil {
			return "", "", fmt.Errorf("服务账号绑定失败：%w", err)
		}
	}
	filter := strings.Replace(cfg.UserFilter, "%s", ldap.EscapeFilter(username), 1)
	res, err := conn.Search(ldap.NewSearchRequest(cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, cfg.TimeoutSeconds, false,
		filter, []string{"dn", "memberOf"}, nil))
	if err != nil {
		return "", "", err
	}
	if len(res.Entries) != 1 {
		return "", "", errors.New("user not found or not unique")
	}
	e := res.Entries[0]
	if err := conn.Bind(e.DN, password); err != nil {
		return "", "", err
	}
	role := cfg.DefaultRole
	groups := map[string]bool{}
	for _, g := range e.GetAttributeValues("memberOf") {
		groups[strings.ToLower(g)] = true
	}
	for _, m := range cfg.RoleMap {
		if groups[strings.ToLower(m.Group)] && rank[m.Role] > rank[role] {
			role = m.Role
		}
	}
	return e.DN, role, nil
}

// ldapLogin 用 LDAP 认证并同步本地账号（来源 ldap）。
func (s *Server) ldapLogin(c *gin.Context, username, password string) (*store.User, bool) {
	cfg := s.ldapConfig()
	if s.Certs == nil {
		return nil, false
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) == 0 || len(username) > 64 {
		return nil, false
	}
	_, role, err := s.ldapAuth(cfg, username, password)
	if err != nil {
		s.Log.Info("ldap login failed", "user", username, "err", err)
		return nil, false
	}
	uq := s.q().User
	ctx := c.Request.Context()
	u, err := uq.WithContext(ctx).Where(uq.Username.Eq(username)).First()
	if err != nil {
		u = &store.User{Username: username, Role: role, Source: "ldap"}
		if err := uq.WithContext(ctx).Create(u); err != nil {
			return nil, false
		}
		s.audit(c, "user.create", "user", username, map[string]string{"source": "ldap", "role": role})
		return u, true
	}
	if u.Source != "ldap" {
		return nil, false // 本地同名账号不被 LDAP 接管
	}
	if u.Role != role {
		_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(u.ID)).Update(uq.Role, role)
		u.Role = role
	}
	return u, true
}

func (s *Server) getLDAP(c *gin.Context) {
	cfg := s.ldapConfig()
	cfg.HasBindPass = cfg.BindPassEnc != ""
	cfg.BindPassEnc, cfg.BindPassword = "", ""
	c.JSON(http.StatusOK, gin.H{"config": cfg, "licensed": s.Lic.Snapshot().Feature(license.FeatureSSO)})
}

func (s *Server) putLDAP(c *gin.Context) {
	var in LDAPConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		failErr(c, err)
		return
	}
	if in.Enabled && !s.Lic.Snapshot().Feature(license.FeatureSSO) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含功能 "+string(license.FeatureSSO))
		return
	}
	if err := in.validate(); err != nil {
		fail(c, http.StatusBadRequest, "BAD_LDAP", err.Error())
		return
	}
	old := s.ldapConfig()
	in.BindPassEnc = old.BindPassEnc
	if in.BindPassword != "" {
		enc, err := s.Certs.Box.Seal(in.BindPassword)
		if err != nil {
			failErr(c, err)
			return
		}
		in.BindPassEnc = enc
	}
	in.BindPassword, in.HasBindPass = "", false
	if in.TimeoutSeconds <= 0 || in.TimeoutSeconds > 60 {
		in.TimeoutSeconds = 10
	}
	b, _ := json.Marshal(in)
	if err := s.Store.SetKV("ldap", string(b)); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "settings.ldap", "settings", "ldap", map[string]any{"enabled": in.Enabled, "url": in.URL, "base_dn": in.BaseDN, "role_map": in.RoleMap})
	s.getLDAP(c)
}

// testLDAP 用给定账号测试认证与角色映射（不创建用户）。
func (s *Server) testLDAP(c *gin.Context) {
	var req credentials
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	dn, role, err := s.ldapAuth(s.ldapConfig(), req.Username, req.Password)
	if err != nil {
		fail(c, http.StatusBadRequest, "LDAP_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"dn": dn, "role": role})
}
