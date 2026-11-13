package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/xiaoguan521/cacheos/internal/console/gateway"
	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

const testPassword = "C0nsole!2026Safe"

// 审计、失败计数写失败不影响请求结果，但要在错误日志里留痕（只写动作名，不写值），并可在指标里看到次数。
func TestAuditAndLoginCounterFailuresAreLoggedNotFatal(t *testing.T) {
	st := storetest.Open(t)
	lic := &licsvc.Service{Store: st, DataDir: t.TempDir(), Keys: license.BuiltinKeyRing()}
	if err := lic.Init(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s := &Server{Store: st, Lic: lic, GW: &gateway.Server{}, Audit: &audit.Logger{DB: st.DB}, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	do := func(method, path string, body any) (int, string) {
		t.Helper()
		var res *http.Response
		var err error
		if body == nil {
			req, _ := http.NewRequest(method, ts.URL+path, nil)
			res, err = client.Do(req)
		} else {
			res, err = client.Do(jsonRequest(t, method, ts.URL+path, body))
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "admin", "password": testPassword}); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}

	// 之后审计表写入、用户表更新都失败
	fail := func(db *gorm.DB) {
		if db.Statement.Table == "audit_logs" || db.Statement.Table == "users" {
			_ = db.AddError(errors.New("boom"))
		}
	}
	if err := st.DB.Callback().Create().Before("gorm:create").Register("t:fail-create", fail); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Callback().Update().Before("gorm:update").Register("t:fail-update", fail); err != nil {
		t.Fatal(err)
	}
	logs.Reset()

	const wrongPW = "WRONG-PW-4417"
	code, body := do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": wrongPW})
	if code != http.StatusUnauthorized || !strings.Contains(body, "BAD_CREDENTIALS") {
		t.Fatalf("failed login: %d %s, want 401 BAD_CREDENTIALS", code, body)
	}
	out := logs.String()
	for _, want := range []string{"level=ERROR", "auth.login", "failed-login counter"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, wrongPW) || strings.Contains(out, testPassword) {
		t.Errorf("log leaks a password:\n%s", out)
	}

	_, metrics := do(http.MethodGet, "/api/v1/metrics/prometheus", nil)
	m := regexp.MustCompile(`(?m)^cacheos_audit_write_failures_total ([0-9]+)$`).FindStringSubmatch(metrics)
	if m == nil || m[1] == "0" {
		t.Fatalf("audit failure counter not exposed or zero:\n%s", metrics)
	}
}

func TestPolicyReadFailureIsLogged(t *testing.T) {
	st := storetest.Open(t)
	var logs bytes.Buffer
	s := &Server{Store: st, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	if p := s.policy(); p.MaxHours == 0 {
		t.Fatalf("missing key must give defaults, got %+v", p)
	}
	if logs.Len() != 0 {
		t.Fatalf("a missing policy key is normal, logged: %s", logs.String())
	}
	sqlDB, _ := st.DB.DB()
	_ = sqlDB.Close()
	if p := s.policy(); p.MaxHours == 0 {
		t.Fatalf("read failure must still fall back to defaults, got %+v", p)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "security_policy") {
		t.Fatalf("not logged: %s", logs.String())
	}
}

func failErrOutcome(t *testing.T, err error) (int, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	var logs bytes.Buffer
	c.Set(logKey, slog.New(slog.NewTextHandler(&logs, nil)))
	failErr(c, err)
	return rec.Code, rec.Body.String(), logs.String()
}

// 服务端故障（数据库、网络、文件、超时）返回 5xx 与通用文案，真实错误只进日志；校验类错误保持 400 与原文。
func TestFailErrHidesServerSideErrors(t *testing.T) {
	const secret = "password authentication failed for user cacheos at 10.0.0.5"
	for name, err := range map[string]error{
		"pg connection": &pgconn.PgError{Code: "08006", Message: secret},
		"pg other":      &pgconn.PgError{Code: "42P01", Message: secret},
		"conn done":     sql.ErrConnDone,
		"deadline":      context.DeadlineExceeded,
		"net":           &net.OpError{Op: "dial", Err: errors.New(secret)},
		"fs":            &fs.PathError{Op: "open", Path: "/var/lib/cacheos/" + secret, Err: errors.New("denied")},
	} {
		code, body, logs := failErrOutcome(t, err)
		if code < 500 || !strings.Contains(body, `"code":"INTERNAL"`) || strings.Contains(body, secret) || strings.Contains(body, "10.0.0.5") {
			t.Errorf("%s: %d %s", name, code, body)
		}
		if !strings.Contains(logs, "level=ERROR") {
			t.Errorf("%s: real error not logged: %q", name, logs)
		}
	}
}

func TestFailErrKeepsClientErrorsAndWrapsServerOnes(t *testing.T) {
	// 校验错误：保持 400 与原文
	code, body, _ := failErrOutcome(t, errors.New("端口必须在 1024-65535 之间"))
	if code != http.StatusBadRequest || !strings.Contains(body, "BAD_REQUEST") || !strings.Contains(body, "端口必须") {
		t.Errorf("validation: %d %s", code, body)
	}
	// 唯一约束冲突属于调用方的问题，保持 400
	code, body, _ = failErrOutcome(t, &pgconn.PgError{Code: "23505", Message: "duplicate key"})
	if code != http.StatusBadRequest {
		t.Errorf("unique violation: %d %s", code, body)
	}
	// 被包装的服务端错误同样识别
	code, body, _ = failErrOutcome(t, errors.Join(errors.New("save unit"), &pgconn.PgError{Code: "08006"}))
	if code < 500 {
		t.Errorf("wrapped: %d %s", code, body)
	}
}
