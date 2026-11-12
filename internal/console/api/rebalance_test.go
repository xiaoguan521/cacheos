package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/orch"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

// countingSender 记录所有下发的指令；再平衡相关的拒绝路径不应到达这里。
type countingSender struct{ sends int }

func (c *countingSender) Send(context.Context, uint, *agentv1.ServerMessage, time.Duration) (*agentv1.CommandResult, error) {
	c.sends++
	return nil, errors.New("unexpected command")
}

func (c *countingSender) Online(uint) bool { return true }

// newRebalanceAPIServer 使用未授权的控制台：与 e2e 测试一样经过真实的中间件链。
func newRebalanceAPIServer(t *testing.T) (*Server, http.Handler, *store.Store, *countingSender) {
	t.Helper()
	st := storetest.Open(t)
	lic := &licsvc.Service{Store: st, DataDir: t.TempDir(), Keys: license.BuiltinKeyRing()}
	if err := lic.Init(); err != nil {
		t.Fatal(err)
	}
	gw := &countingSender{}
	s := &Server{Store: st, Lic: lic, Audit: &audit.Logger{DB: st.DB},
		Orch: &orch.Service{Store: st, GW: gw, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return s, h, st, gw
}

// sessionFor 为指定角色的账号签发会话 Cookie（与登录后签发的令牌相同的结构与密钥）。
func sessionFor(t *testing.T, s *Server, st *store.Store, role string) *http.Cookie {
	t.Helper()
	u := &store.User{Username: fmt.Sprintf("user-%s-%d", role, time.Now().UnixNano()), Role: role, PasswordHash: "-"}
	if err := st.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{UID: u.ID, Name: u.Username, Role: role, AuthAt: time.Now().Unix(),
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now())}})
	str, err := tok.SignedString(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: cookieName, Value: str}
}

func TestRebalanceRejectsInsufficientRole(t *testing.T) {
	s, h, st, gw := newRebalanceAPIServer(t)
	// 角色等级低于 releaser 的账号都必须被拒绝（operator 等级高于 releaser，与 reshard 路由一致，故不在此列）。
	for _, role := range []string{store.RoleViewer, store.RoleAuditor, store.RoleDeveloper} {
		for _, body := range []map[string]any{{"dry_run": true}, {"dry_run": false, "batch": 100}} {
			req := jsonRequest(t, http.MethodPost, "/api/v1/units/1/rebalance", body)
			req.AddCookie(sessionFor(t, s, st, role))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("role %s body %v: status %d %s, want 403", role, body, rec.Code, rec.Body.String())
			}
		}
	}
	if gw.sends != 0 {
		t.Fatalf("rejected requests sent %d commands", gw.sends)
	}
}

func TestRebalanceIsBlockedWhenUnlicensed(t *testing.T) {
	// 未授权（或只读）时写接口与再平衡共用同一个可写性门禁，不得因为 dry_run 而放行。
	s, h, st, gw := newRebalanceAPIServer(t)
	for _, body := range []map[string]any{{"dry_run": true}, {"dry_run": false, "batch": 100}} {
		req := jsonRequest(t, http.MethodPost, "/api/v1/units/1/rebalance", body)
		req.AddCookie(sessionFor(t, s, st, store.RoleReleaser))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "LICENSE_READ_ONLY") {
			t.Fatalf("body %v: status %d %s, want 423 LICENSE_READ_ONLY", body, rec.Code, rec.Body.String())
		}
	}
	if gw.sends != 0 {
		t.Fatalf("unlicensed requests sent %d commands", gw.sends)
	}
	var n int64
	if err := st.DB.Model(&store.Operation{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unlicensed requests created %d operations", n)
	}
}

func TestRebalanceRouteUsesReshardGuards(t *testing.T) {
	// 路由与 reshard 使用同一组中间件；OpenAPI 由路由表生成，应能看到新路径。
	s, h, st, _ := newRebalanceAPIServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	req.AddCookie(sessionFor(t, s, st, store.RoleViewer))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/units/{id}/rebalance") {
		t.Fatal("openapi.json does not list POST /units/{id}/rebalance")
	}
}
