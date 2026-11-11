package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

const (
	cliArgMarker   = "ARG-MARKER-7f3a91"   // 参数值中的标记：不得出现在审计、日志中
	cliReplyMarker = "REPLY-MARKER-c42e08" // 回复中的标记：不得出现在审计、日志中
)

type cliFixture struct {
	s    *Server
	user store.User
	in   store.Instance
	logs *bytes.Buffer
	sent []*agentv1.ServerMessage
	// reply 为 agent 回复的 payload；nil 时返回 OK 与简单字符串
	reply      []byte
	code       string // 非空时 agent 返回该错误码
	totpSecret string
}

func newCLIFixture(t *testing.T, cliExec, withTOTP bool) *cliFixture {
	t.Helper()
	st := storetest.Open(t)
	f := &cliFixture{logs: &bytes.Buffer{}}
	f.s = &Server{Store: st, Audit: &audit.Logger{DB: st.DB}, Log: slog.New(slog.NewTextHandler(f.logs, nil))}
	f.s.execSend = f.send
	f.user = store.User{Username: "dev", Role: store.RoleDeveloper, CliExec: cliExec}
	if err := st.DB.Create(&f.user).Error; err != nil {
		t.Fatal(err)
	}
	unit := store.Unit{Name: "cache-a", Mode: "standalone"}
	if err := st.DB.Create(&unit).Error; err != nil {
		t.Fatal(err)
	}
	f.in = store.Instance{UnitID: unit.ID, HostID: 9, Port: 7001, Kind: "server"}
	if err := st.DB.Create(&f.in).Error; err != nil {
		t.Fatal(err)
	}
	if withTOTP {
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "CacheOS", AccountName: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		f.totpSecret = key.Secret()
		if err := st.SetKV(f.s.totpKey(f.user.ID), f.totpSecret); err != nil {
			t.Fatal(err)
		}
		if err := st.SetKV(f.s.totpEnabledKey(f.user.ID), "1"); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// otp 返回当前有效的 TOTP 验证码。
func (f *cliFixture) otp(t *testing.T) string {
	t.Helper()
	code, err := totp.GenerateCode(f.totpSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (f *cliFixture) send(_ context.Context, hostID uint, m *agentv1.ServerMessage, _ time.Duration) (*agentv1.CommandResult, error) {
	f.sent = append(f.sent, m)
	if f.code != "" {
		return &agentv1.CommandResult{Status: agentv1.CommandResult_FAILED, ErrorCode: f.code, Message: "agent refused"}, nil
	}
	payload := f.reply
	if payload == nil {
		payload = []byte(`{"reply":{"type":"simple","value":"OK"},"truncated":false}`)
	}
	return &agentv1.CommandResult{Status: agentv1.CommandResult_OK, Payload: payload}, nil
}

// router 复现正式路由的中间件链（不含 writable：测试不构造授权）。uid 对应 f.user。
func (f *cliFixture) router() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("uid", f.user.ID)
		c.Set("user", f.user.Username)
		c.Set("role", f.user.Role)
		c.Next()
	})
	r.POST("/api/v1/units/:id/exec", role(store.RoleDeveloper), f.s.stepUp, f.s.requireTOTP, f.s.execCommand)
	return r
}

func (f *cliFixture) post(t *testing.T, body any, totpCode string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/units/"+strconv.Itoa(int(f.in.UnitID))+"/exec?instance_id="+strconv.Itoa(int(f.in.ID)), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if totpCode != "" {
		req.Header.Set("X-TOTP-Code", totpCode)
	}
	w := httptest.NewRecorder()
	f.router().ServeHTTP(w, req)
	return w
}

func (f *cliFixture) auditRows(t *testing.T) []store.AuditLog {
	t.Helper()
	var rows []store.AuditLog
	if err := f.s.Store.DB.Where("action = ?", "unit.cli.exec").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func errorCodeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	return e.Code
}

func TestExecRequiresCliExecGrant(t *testing.T) {
	f := newCLIFixture(t, false, true)
	w := f.post(t, map[string]any{"argv": []string{"GET", "k"}}, f.otp(t))
	if w.Code != http.StatusForbidden || errorCodeOf(t, w) != "CLI_EXEC_DENIED" {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if len(f.sent) != 0 {
		t.Fatal("command dispatched without grant")
	}
	rows := f.auditRows(t)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"status":"CLI_EXEC_DENIED"`) {
		t.Fatalf("denied request not audited: %+v", rows)
	}
}

func TestExecRequiresSecondFactor(t *testing.T) {
	f := newCLIFixture(t, true, true)
	argv := map[string]any{"argv": []string{"GET", "k"}}

	if w := f.post(t, argv, ""); w.Code != http.StatusUnauthorized || errorCodeOf(t, w) != "TOTP_STEP_UP_REQUIRED" {
		t.Fatalf("no code: %d %s", w.Code, w.Body.String())
	}
	wrong := "000000"
	if f.otp(t) == wrong {
		wrong = "111111"
	}
	if w := f.post(t, argv, wrong); w.Code != http.StatusUnauthorized || errorCodeOf(t, w) != "TOTP_STEP_UP_INVALID" {
		t.Fatalf("bad code: %d %s", w.Code, w.Body.String())
	}
	if len(f.sent) != 0 {
		t.Fatal("dispatched without a valid second factor")
	}

	// 未启用 TOTP 的用户同样被拒绝（stepUp 在未启用时放行，这里补上 fail-closed）
	g := newCLIFixture(t, true, false)
	if w := g.post(t, argv, ""); w.Code != http.StatusForbidden || errorCodeOf(t, w) != "TOTP_REQUIRED" {
		t.Fatalf("no totp enrolled: %d %s", w.Code, w.Body.String())
	}
	if len(g.sent) != 0 {
		t.Fatal("dispatched for user without TOTP")
	}

	// 正确验证码通过
	if w := f.post(t, argv, f.otp(t)); w.Code != http.StatusOK {
		t.Fatalf("valid code: %d %s", w.Code, w.Body.String())
	}
}

func TestExecRejectsDenylistWithoutDispatch(t *testing.T) {
	f := newCLIFixture(t, true, true)
	code := f.otp(t)
	cases := []struct {
		argv []string
		want string
		http int
	}{
		{[]string{"FLUSHALL"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"flushall", "async"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"CoNfIg", "GET", "*"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"client", "list"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"EVAL", "return 1", "0"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"BLPOP", "q", "0"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"XREAD", "BLOCK", "0", "STREAMS", "s", "$"}, "CLI_COMMAND_DENIED", http.StatusForbidden},
		{[]string{"GE T", "k"}, "CLI_ARGV_INVALID", http.StatusBadRequest},
		{[]string{"GET\r\nFLUSHALL"}, "CLI_ARGV_INVALID", http.StatusBadRequest},
		{[]string{}, "CLI_ARGV_INVALID", http.StatusBadRequest},
	}
	for _, c := range cases {
		w := f.post(t, map[string]any{"argv": c.argv}, code)
		if w.Code != c.http || errorCodeOf(t, w) != c.want {
			t.Errorf("%q: %d %s", c.argv, w.Code, w.Body.String())
		}
	}
	if len(f.sent) != 0 {
		t.Fatalf("%d rejected commands were dispatched to the agent", len(f.sent))
	}
	if n := len(f.auditRows(t)); n != len(cases) {
		t.Fatalf("audit rows %d, want %d", n, len(cases))
	}
}

func TestExecDispatchesWellFormedCommand(t *testing.T) {
	f := newCLIFixture(t, true, true)
	f.reply = []byte(`{"reply":{"type":"array","value":[{"type":"bulk","value":"` + cliReplyMarker + `"},{"type":"integer","value":3}]},"truncated":true}`)
	argv := []string{"HGETALL", "h1"}
	w := f.post(t, map[string]any{"argv": argv}, f.otp(t))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(f.sent) != 1 {
		t.Fatalf("dispatched %d messages", len(f.sent))
	}
	ex, ok := f.sent[0].Body.(*agentv1.ServerMessage_ExecCommand)
	if !ok {
		t.Fatalf("body %T", f.sent[0].Body)
	}
	if ex.ExecCommand.GetPort() != 7001 || strings.Join(ex.ExecCommand.GetArgv(), "|") != "HGETALL|h1" {
		t.Fatalf("exec command %+v", ex.ExecCommand)
	}
	// 返回给浏览器的结构保持原样
	var got struct {
		Reply struct {
			Type string `json:"type"`
		} `json:"reply"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Reply.Type != "array" || !got.Truncated {
		t.Fatalf("response %s", w.Body.String())
	}
	rows := f.auditRows(t)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"command":"HGETALL"`) || !strings.Contains(rows[0].Detail, `"argc":1`) ||
		!strings.Contains(rows[0].Detail, `"status":"OK"`) || !strings.Contains(rows[0].Detail, `"reply_type":"array"`) {
		t.Fatalf("audit rows %+v", rows)
	}
}

func TestExecAuditAndLogsOmitValues(t *testing.T) {
	f := newCLIFixture(t, true, true)
	code := f.otp(t)
	f.reply = []byte(`{"reply":{"type":"bulk","value":"` + cliReplyMarker + `"},"truncated":false}`)
	if w := f.post(t, map[string]any{"argv": []string{"SET", "secret-key", cliArgMarker}}, code); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	if w := f.post(t, map[string]any{"argv": []string{"FLUSHALL", cliArgMarker}}, code); w.Code != http.StatusForbidden {
		t.Fatalf("denied: %d", w.Code)
	}
	var all []store.AuditLog
	if err := f.s.Store.DB.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range all {
		b, _ := json.Marshal(row)
		if strings.Contains(string(b), cliArgMarker) || strings.Contains(string(b), cliReplyMarker) || strings.Contains(string(b), "secret-key") {
			t.Fatalf("audit leaks values: %s", b)
		}
	}
	if len(all) != 2 {
		t.Fatalf("audit rows %d", len(all))
	}
	if strings.Contains(f.logs.String(), cliArgMarker) || strings.Contains(f.logs.String(), cliReplyMarker) {
		t.Fatalf("log leaks values: %s", f.logs.String())
	}
}

func TestExecAgentRefusalIsReported(t *testing.T) {
	f := newCLIFixture(t, true, true)
	f.code = "TIMEOUT"
	w := f.post(t, map[string]any{"argv": []string{"GET", "k"}}, f.otp(t))
	if w.Code != http.StatusConflict || errorCodeOf(t, w) != "TIMEOUT" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := f.auditRows(t)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"status":"TIMEOUT"`) {
		t.Fatalf("refusal not audited: %+v", rows)
	}
}

func TestUpdateUserCliExecOnlyForDeveloperOrAdmin(t *testing.T) {
	f := newCLIFixture(t, false, false)
	viewer := store.User{Username: "viewer", Role: store.RoleViewer}
	if err := f.s.Store.DB.Create(&viewer).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	body, _ := json.Marshal(map[string]any{"cli_exec": true})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/"+strconv.Itoa(int(viewer.ID)), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(viewer.ID))}}
	c.Set("uid", f.user.ID)
	c.Set("user", "admin")
	f.s.updateUser(c)
	if w.Code != http.StatusBadRequest || errorCodeOf(t, w) != "CLI_EXEC_ROLE" {
		t.Fatalf("viewer grant: %d %s", w.Code, w.Body.String())
	}
}
