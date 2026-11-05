package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// 开放 API 令牌：Authorization: Bearer etos_<随机>。只存哈希；令牌角色不高于所属用户的当前角色；
// 用户被删除或降级后令牌随之失效或降级；需要授权功能 api_token。

const tokenPrefix = "etos_"

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Server) tokenAuth(c *gin.Context) {
	raw := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if !strings.HasPrefix(raw, tokenPrefix) || len(raw) > 128 {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
		return
	}
	if !s.Lic.Snapshot().Feature(license.FeatureAPIToken) {
		fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含功能 "+string(license.FeatureAPIToken))
		return
	}
	q := s.q()
	ctx := c.Request.Context()
	t, err := q.APIToken.WithContext(ctx).Where(q.APIToken.TokenHash.Eq(hashToken(raw))).First()
	if err != nil || t.Revoked || (t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt)) {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
		return
	}
	u, err := q.User.WithContext(ctx).Where(q.User.Username.Eq(t.Username)).First()
	if err != nil || u.Disabled {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "token owner no longer exists")
		return
	}
	role := t.Role
	if rank[u.Role] < rank[role] {
		role = u.Role // 用户被降级后，令牌权限随之降级
	}
	now := time.Now()
	_, _ = q.APIToken.WithContext(ctx).Where(q.APIToken.ID.Eq(t.ID)).UpdateSimple(q.APIToken.LastUsedAt.Value(now), q.APIToken.LastUsedIP.Value(c.ClientIP()))
	c.Set("uid", u.ID)
	c.Set("user", u.Username)
	c.Set("role", role)
	c.Set("token", t.Name)
	c.Next()
}

func (s *Server) listTokens(c *gin.Context) {
	q := s.q()
	sel := q.APIToken.WithContext(c.Request.Context()).Order(q.APIToken.ID.Desc())
	if c.GetString("role") != store.RoleAdmin {
		sel = sel.Where(q.APIToken.Username.Eq(c.GetString("user")))
	}
	rows, err := sel.Find()
	if err != nil {
		failErr(c, err)
		return
	}
	if rows == nil {
		rows = []*store.APIToken{}
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) createToken(c *gin.Context) {
	if c.GetString("token") != "" {
		fail(c, http.StatusForbidden, "FORBIDDEN", "令牌不能创建令牌，请登录控制台创建")
		return
	}
	var req struct {
		Name       string `json:"name"`
		Role       string `json:"role"`
		ExpireDays int    `json:"expire_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		fail(c, http.StatusBadRequest, "BAD_REQUEST", "令牌名称必填，最长 64 个字符")
		return
	}
	if req.Role == "" {
		req.Role = c.GetString("role")
	}
	if _, ok := rank[req.Role]; !ok || rank[req.Role] > rank[c.GetString("role")] {
		fail(c, http.StatusBadRequest, "BAD_REQUEST", "令牌角色不能高于自己的角色")
		return
	}
	if req.ExpireDays < 0 || req.ExpireDays > 3650 {
		fail(c, http.StatusBadRequest, "BAD_REQUEST", "有效期为 0（不过期）到 3650 天")
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		failErr(c, err)
		return
	}
	raw := tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	t := &store.APIToken{Name: req.Name, Username: c.GetString("user"), Role: req.Role, TokenHash: hashToken(raw), Prefix: raw[:12], CreatedBy: c.GetString("user")}
	if req.ExpireDays > 0 {
		exp := time.Now().Add(time.Duration(req.ExpireDays) * 24 * time.Hour)
		t.ExpiresAt = &exp
	}
	if err := s.q().APIToken.WithContext(c.Request.Context()).Create(t); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "api_token.create", "api_token", fmt.Sprint(t.ID), map[string]any{"name": t.Name, "role": t.Role, "prefix": t.Prefix, "expires_at": t.ExpiresAt})
	c.JSON(http.StatusOK, gin.H{"token": raw, "info": t}) // 明文只返回这一次
}

func (s *Server) revokeToken(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	q := s.q()
	t, err := q.APIToken.WithContext(c.Request.Context()).Where(q.APIToken.ID.Eq(id)).First()
	if err != nil {
		failErr(c, store.ErrNotFound)
		return
	}
	if t.Username != c.GetString("user") && c.GetString("role") != store.RoleAdmin {
		fail(c, http.StatusForbidden, "FORBIDDEN", "只能撤销自己的令牌")
		return
	}
	if _, err := q.APIToken.WithContext(c.Request.Context()).Where(q.APIToken.ID.Eq(id)).Update(q.APIToken.Revoked, true); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "api_token.revoke", "api_token", fmt.Sprint(id), map[string]any{"name": t.Name, "owner": t.Username})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// openAPI 根据实际注册的路由生成 OpenAPI 3 文档（路径、方法与认证方式），始终与实现一致。
func (s *Server) openAPI(c *gin.Context) {
	paths := map[string]map[string]any{}
	var routes []gin.RouteInfo
	if s.routes != nil {
		routes = s.routes()
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Path < routes[j].Path })
	for _, r := range routes {
		if !strings.HasPrefix(r.Path, "/api/v1/") {
			continue
		}
		p := strings.TrimPrefix(r.Path, "/api/v1")
		var params []map[string]any
		segs := strings.Split(p, "/")
		for i, seg := range segs {
			if strings.HasPrefix(seg, ":") {
				params = append(params, map[string]any{"name": seg[1:], "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
				segs[i] = "{" + seg[1:] + "}"
			}
		}
		p = strings.Join(segs, "/")
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		op := map[string]any{"operationId": strings.ToLower(r.Method) + strings.ReplaceAll(strings.ReplaceAll(p, "/", "_"), "{", ""),
			"responses": map[string]any{"200": map[string]string{"description": "OK"}, "401": map[string]string{"description": "未认证"},
				"402": map[string]string{"description": "授权不包含该功能"}, "403": map[string]string{"description": "角色不足"}, "423": map[string]string{"description": "授权只读"}}}
		if params != nil {
			op["parameters"] = params
		}
		paths[p][strings.ToLower(r.Method)] = op
	}
	c.JSON(http.StatusOK, gin.H{
		"openapi": "3.0.3",
		"info":    gin.H{"title": "EdgeTraffic OS Console API", "version": "v1", "description": "使用 Authorization: Bearer <令牌> 调用；修改类接口在控制台会话中还需 X-Requested-With: etos。"},
		"servers": []gin.H{{"url": "/api/v1"}},
		"components": gin.H{"securitySchemes": gin.H{"bearer": gin.H{"type": "http", "scheme": "bearer"},
			"cookie": gin.H{"type": "apiKey", "in": "cookie", "name": cookieName}}},
		"security": []gin.H{{"bearer": []string{}}, {"cookie": []string{}}},
		"paths":    paths,
	})
}
