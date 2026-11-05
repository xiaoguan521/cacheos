package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/xiaoguan521/cacheos/internal/console/oidc"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// OIDC 单点登录（授权功能 sso）：授权码 + PKCE；用户首次登录自动创建（来源 oidc），角色按声明映射并每次刷新。

// OIDCConfig 是 OIDC 设置（ClientSecret 只写不读，加密保存）。
type OIDCConfig struct {
	Enabled       bool          `json:"enabled"`
	Issuer        string        `json:"issuer"`
	ClientID      string        `json:"client_id"`
	ClientSecret  string        `json:"client_secret,omitempty"`
	SecretEnc     string        `json:"client_secret_enc,omitempty"`
	RedirectURL   string        `json:"redirect_url"` // https://<控制台>/api/v1/auth/oidc/callback
	UsernameClaim string        `json:"username_claim"`
	RoleClaim     string        `json:"role_claim"`
	RoleMap       []LDAPRoleMap `json:"role_map"` // Group 字段为声明值
	DefaultRole   string        `json:"default_role"`
	HasSecret     bool          `json:"has_client_secret"`
}

var oidcClient = &oidc.Client{}

var oidcUserRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{0,63}$`)

func (s *Server) oidcConfig() OIDCConfig {
	var c OIDCConfig
	if v, err := s.Store.GetKV("oidc"); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	if c.UsernameClaim == "" {
		c.UsernameClaim = "preferred_username"
	}
	if c.RoleClaim == "" {
		c.RoleClaim = "groups"
	}
	if c.DefaultRole == "" {
		c.DefaultRole = store.RoleViewer
	}
	return c
}

func (s *Server) oidcEnabled() bool {
	return s.oidcConfig().Enabled && s.Lic.Snapshot().Feature(license.FeatureSSO)
}

func (s *Server) oidcClientConfig(c OIDCConfig) (oidc.Config, error) {
	secret := ""
	if c.SecretEnc != "" {
		if s.Box == nil {
			return oidc.Config{}, errors.New("secret box is not configured")
		}
		v, err := s.Box.Open(c.SecretEnc)
		if err != nil {
			return oidc.Config{}, err
		}
		secret = v
	}
	return oidc.Config{Issuer: c.Issuer, ClientID: c.ClientID, ClientSecret: secret, RedirectURL: c.RedirectURL}, nil
}

type flowClaims struct {
	oidc.Flow
	jwt.RegisteredClaims
}

const oidcCookie = "etos_oidc"

func (s *Server) oidcStart(c *gin.Context) {
	if !s.oidcEnabled() {
		fail(c, http.StatusNotFound, "OIDC_DISABLED", "未启用单点登录")
		return
	}
	cfg, err := s.oidcClientConfig(s.oidcConfig())
	if err != nil {
		failErr(c, err)
		return
	}
	u, f, err := oidcClient.Start(c.Request.Context(), cfg)
	if err != nil {
		fail(c, http.StatusBadGateway, "OIDC_UNAVAILABLE", "身份提供方不可用："+err.Error())
		return
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, flowClaims{Flow: f, RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)), Subject: "oidc-flow"}}).SignedString(s.jwtKey)
	if err != nil {
		failErr(c, err)
		return
	}
	// 回调是来自身份提供方的跨站跳转，暂存 Cookie 用 Lax 才能随回调带回；只含一次性随机值
	http.SetCookie(c.Writer, &http.Cookie{Name: oidcCookie, Value: tok, Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: s.SecureCookie,
		SameSite: http.SameSiteLaxMode, MaxAge: 600})
	c.Redirect(http.StatusFound, u)
}

func (s *Server) oidcCallback(c *gin.Context) {
	bad := func(msg string) {
		s.audit(c, "auth.login", "user", "oidc", map[string]string{"result": "failure", "reason": msg})
		c.Redirect(http.StatusFound, "/login?sso_error="+url.QueryEscape(msg))
	}
	if !s.oidcEnabled() {
		bad("未启用单点登录")
		return
	}
	ck, err := c.Cookie(oidcCookie)
	http.SetCookie(c.Writer, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/oidc", MaxAge: -1, HttpOnly: true})
	if err != nil {
		bad("登录已过期，请重试")
		return
	}
	var fc flowClaims
	if t, err := jwt.ParseWithClaims(ck, &fc, func(t *jwt.Token) (any, error) { return s.jwtKey, nil }, jwt.WithValidMethods([]string{"HS256"})); err != nil || !t.Valid || fc.Subject != "oidc-flow" {
		bad("登录已过期，请重试")
		return
	}
	conf := s.oidcConfig()
	cfg, err := s.oidcClientConfig(conf)
	if err != nil {
		bad(err.Error())
		return
	}
	claims, err := oidcClient.Finish(c.Request.Context(), cfg, fc.Flow, c.Query("state"), c.Query("code"))
	if err != nil {
		bad("身份校验失败：" + err.Error())
		return
	}
	names := oidc.Strings(claims, conf.UsernameClaim)
	if len(names) == 0 {
		bad("身份令牌中没有用户名声明 " + conf.UsernameClaim)
		return
	}
	username := strings.ToLower(names[0])
	if !oidcUserRe.MatchString(username) {
		bad("用户名格式不受支持：" + username)
		return
	}
	role := conf.DefaultRole
	vals := map[string]bool{}
	for _, v := range oidc.Strings(claims, conf.RoleClaim) {
		vals[v] = true
	}
	for _, m := range conf.RoleMap {
		if vals[m.Group] && rank[m.Role] > rank[role] {
			role = m.Role
		}
	}
	u, err := s.syncExternalUser(c, username, "oidc", role)
	if err != nil {
		bad(err.Error())
		return
	}
	if u.Disabled {
		bad("账号已停用")
		return
	}
	uq := s.q().User
	_, _ = uq.WithContext(c.Request.Context()).Where(uq.ID.Eq(u.ID)).Update(uq.LastLoginAt, time.Now())
	c.Set("user", u.Username)
	s.audit(c, "auth.login", "user", u.Username, map[string]string{"source": "oidc", "role": u.Role})
	if err := s.issue(c, u); err != nil {
		bad(err.Error())
		return
	}
	c.Redirect(http.StatusFound, "/dashboard")
}

// syncExternalUser 创建或更新外部身份账号；本地同名账号不被接管。
func (s *Server) syncExternalUser(c *gin.Context, username, source, role string) (*store.User, error) {
	uq := s.q().User
	ctx := c.Request.Context()
	u, err := uq.WithContext(ctx).Where(uq.Username.Eq(username)).First()
	if err != nil {
		u = &store.User{Username: username, Role: role, Source: source}
		if err := uq.WithContext(ctx).Create(u); err != nil {
			return nil, err
		}
		s.audit(c, "user.create", "user", username, map[string]string{"source": source, "role": role})
		return u, nil
	}
	if u.Source != source {
		return nil, errors.New("已存在同名的本地账号，不能用单点登录接管")
	}
	if u.Role != role {
		_, _ = uq.WithContext(ctx).Where(uq.ID.Eq(u.ID)).Update(uq.Role, role)
		u.Role = role
	}
	return u, nil
}

func (s *Server) getOIDC(c *gin.Context) {
	cfg := s.oidcConfig()
	cfg.HasSecret = cfg.SecretEnc != ""
	cfg.SecretEnc, cfg.ClientSecret = "", ""
	c.JSON(http.StatusOK, gin.H{"config": cfg, "licensed": s.Lic.Snapshot().Feature(license.FeatureSSO)})
}

func (s *Server) putOIDC(c *gin.Context) {
	var in OIDCConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		failErr(c, err)
		return
	}
	if in.Enabled && !s.Lic.Snapshot().Feature(license.FeatureSSO) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含功能 "+string(license.FeatureSSO))
		return
	}
	iu, err1 := url.Parse(in.Issuer)
	ru, err2 := url.Parse(in.RedirectURL)
	if err1 != nil || iu.Scheme != "https" || err2 != nil || (ru.Scheme != "https" && ru.Hostname() != "localhost") ||
		!strings.HasSuffix(ru.Path, "/api/v1/auth/oidc/callback") || in.ClientID == "" {
		fail(c, http.StatusBadRequest, "BAD_OIDC", "Issuer 与回调地址必须是 https，回调地址以 /api/v1/auth/oidc/callback 结尾，Client ID 必填")
		return
	}
	if _, ok := rank[in.DefaultRole]; !ok && in.DefaultRole != "" {
		fail(c, http.StatusBadRequest, "BAD_OIDC", "默认角色无效")
		return
	}
	old := s.oidcConfig()
	in.SecretEnc = old.SecretEnc
	if in.ClientSecret != "" {
		if s.Box == nil {
			fail(c, http.StatusInternalServerError, "SECRET_BOX_UNAVAILABLE", "服务端加密密钥未就绪")
			return
		}
		enc, err := s.Box.Seal(in.ClientSecret)
		if err != nil {
			failErr(c, err)
			return
		}
		in.SecretEnc = enc
	}
	in.ClientSecret, in.HasSecret = "", false
	b, _ := json.Marshal(in)
	if err := s.Store.SetKV("oidc", string(b)); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "settings.oidc", "settings", "oidc", map[string]any{"enabled": in.Enabled, "issuer": in.Issuer, "client_id": in.ClientID})
	s.getOIDC(c)
}
