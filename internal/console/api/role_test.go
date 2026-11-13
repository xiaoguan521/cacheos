package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 路由上写错角色名（例如拼写错误）不能悄悄变成“所有人都可访问”：注册路由时就应失败。
func TestRoleRejectsUnknownMinimumRoleAtRegistration(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal(`role("admn") did not panic; a mistyped role name would let everyone through`)
		}
	}()
	role("admn")
}

// 令牌里的角色名不认识（或为空）时，要求任何已知角色的路由都必须拒绝。
func TestRoleDeniesUnknownCallerRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, callerRole := range []string{"", "root", "Admin"} {
		r := gin.New()
		r.GET("/x", func(c *gin.Context) { c.Set("role", callerRole) }, role("viewer"), func(c *gin.Context) { c.Status(http.StatusOK) })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("caller role %q: status %d, want 403", callerRole, rec.Code)
		}
	}
}
