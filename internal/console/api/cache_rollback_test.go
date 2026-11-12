package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/xiaoguan521/cacheos/internal/console/orch"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

// 回滚路由必须和"应用参数"一样要求 releaser 角色：developer 应在进入处理函数前被 403 拒绝。
func TestRollbackRouteRequiresReleaserRole(t *testing.T) {
	st := storetest.Open(t)
	s := &Server{Store: st, Audit: &audit.Logger{DB: st.DB}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	dev := store.User{Username: "dev1", Role: store.RoleDeveloper}
	if err := st.DB.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{UID: dev.ID, Name: dev.Username, Role: dev.Role, AuthAt: time.Now().Unix(),
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})
	str, err := tok.SignedString(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/units/1/revisions/p00000001/rollback", strings.NewReader(`{"parent_revision_id":"p00000002","note":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "cacheos")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: str})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"FORBIDDEN"`) {
		t.Fatalf("developer rollback: %d %s, want 403 FORBIDDEN", rec.Code, rec.Body.String())
	}
}

func TestRollbackErrorsMapToConflictCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{orch.ErrRevisionConflict, "REVISION_CONFLICT"},
		{orch.ErrSameRevision, "SAME_REVISION"},
	}
	gin.SetMode(gin.TestMode)
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		failErr(c, tc.err)
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusConflict || body.Code != tc.code {
			t.Fatalf("%v -> %d %s, want 409 %s", tc.err, rec.Code, body.Code, tc.code)
		}
	}
}
