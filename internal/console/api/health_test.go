package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xiaoguan521/cacheos/internal/console/ha"
	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func haRouter(t *testing.T, el *ha.Elector) *gin.Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	s := &Server{Store: st, HA: el}
	r := gin.New()
	r.GET("/healthz", s.healthz)
	r.Use(s.standbyGuard)
	r.GET("/api/v1/x", func(c *gin.Context) { c.String(200, "ok") })
	return r
}

func get(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestStandbyRejectsAPIButAnswersHealthz(t *testing.T) {
	r := haRouter(t, &ha.Elector{}) // 未运行选主：不是主
	if w := get(r, "/api/v1/x"); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("standby api = %d %s", w.Code, w.Body)
	}
	if w := get(r, "/healthz"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("standby healthz = %d", w.Code)
	}
}

func TestLeaderServes(t *testing.T) {
	el := &ha.Elector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go el.Run(ctx, func(context.Context) {}, nil)
	for i := 0; !el.IsLeader(); i++ {
		if i > 100 {
			t.Fatal("never became leader")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := haRouter(t, el)
	if w := get(r, "/api/v1/x"); w.Code != 200 {
		t.Fatalf("leader api = %d", w.Code)
	}
	if w := get(r, "/healthz"); w.Code != 200 {
		t.Fatalf("leader healthz = %d %s", w.Code, w.Body)
	}
}
