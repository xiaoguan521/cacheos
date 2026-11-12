package main

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testToken = "etos_TESTTOKEN0123456789"

// fakeConsole 记录收到的请求，并按路径返回预设的响应。
type fakeConsole struct {
	mu       sync.Mutex
	requests []string
	auth     []string
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if h, ok := f.routes[r.URL.Path]; ok {
		h(w, r)
		return
	}
	http.Error(w, `{"code":"NOT_FOUND","message":"not found"}`, http.StatusNotFound)
}

func jsonHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// runCLI 在给定环境下执行 CLI，返回退出码与标准输出、标准错误。
func runCLI(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string { return env[k] }
	code := run(args, getenv, &out, &errb)
	return code, out.String(), errb.String()
}

func newFake(t *testing.T) (*httptest.Server, *fakeConsole) {
	t.Helper()
	f := &fakeConsole{routes: map[string]func(http.ResponseWriter, *http.Request){}}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	return srv, f
}

// caFile 把测试服务器的证书写成 PEM 文件，供 --ca-file 使用。
func caFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(url string) map[string]string {
	return map[string]string{"CACHEOS_URL": url, "CACHEOS_TOKEN": testToken}
}

// 令牌只能来自环境变量，不接受命令行参数（会进入 ps 与 shell 历史）。
func TestTokenFlagIsRejected(t *testing.T) {
	code, out, errb := runCLI(t, map[string]string{"CACHEOS_URL": "https://127.0.0.1:1"}, "--token", testToken, "units", "ls")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if strings.Contains(out+errb, testToken) {
		t.Fatal("token leaked into output")
	}
}

func TestMissingTokenFails(t *testing.T) {
	code, _, errb := runCLI(t, map[string]string{"CACHEOS_URL": "https://127.0.0.1:1"}, "units", "ls")
	if code != 2 || !strings.Contains(errb, "CACHEOS_TOKEN") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
}

func TestMissingURLFails(t *testing.T) {
	code, _, errb := runCLI(t, map[string]string{"CACHEOS_TOKEN": testToken}, "units", "ls")
	if code != 2 || !strings.Contains(errb, "CACHEOS_URL") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
}

// 控制台只接受 https，明文 http 会把令牌暴露在网络上。
func TestPlainHTTPURLIsRejected(t *testing.T) {
	code, _, errb := runCLI(t, map[string]string{"CACHEOS_URL": "http://console.example:8443", "CACHEOS_TOKEN": testToken}, "units", "ls")
	if code != 2 || !strings.Contains(errb, "https") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
}

func TestUnitsListSendsBearerToken(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/units"] = jsonHandler(`[{"id":3,"name":"orders","mode":"cluster","shards":3,"replicas":1,"memory_mb":2048,"health":"ok"}]`)
	code, out, errb := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "units", "ls")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
	if got := f.auth[0]; got != "Bearer "+testToken {
		t.Fatalf("Authorization = %q", got)
	}
	if !strings.Contains(out, "orders") || !strings.Contains(out, "cluster") {
		t.Fatalf("table missing fields: %q", out)
	}
	if strings.Contains(out, testToken) {
		t.Fatal("token printed")
	}
}

func TestHostsListUsesHostsEndpoint(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/hosts"] = jsonHandler(`[{"id":1,"name":"node-a","status":"online","online":true,"agent_version":"0.4.5"}]`)
	code, out, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "hosts", "ls")
	if code != 0 || !strings.Contains(out, "node-a") || !strings.Contains(out, "0.4.5") {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
}

func TestUnitGetRequestsSingleUnit(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/units/3"] = jsonHandler(`{"id":3,"name":"orders","health":"warning","problems":["内存接近上限"]}`)
	code, out, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "units", "get", "3")
	if code != 0 || !strings.Contains(out, "warning") || !strings.Contains(out, "内存接近上限") {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
	if f.requests[0] != "GET /api/v1/units/3" {
		t.Fatalf("request = %q", f.requests[0])
	}
}

// 实例 ID 只接受正整数，防止路径注入（例如 ../ 或查询串拼接）。
func TestInvalidIDIsRejectedBeforeRequest(t *testing.T) {
	srv, f := newFake(t)
	for _, bad := range []string{"abc", "0", "-1", "3/../x", "3?x=1", "", "99999999999999999999"} {
		code, _, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "units", "get", bad)
		if code != 2 {
			t.Fatalf("id %q: exit = %d, want 2", bad, code)
		}
	}
	if len(f.requests) != 0 {
		t.Fatalf("requests were sent for invalid ids: %v", f.requests)
	}
}

func TestOpsListPassesUnitFilter(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/operations"] = jsonHandler(`[{"id":9,"unit_id":3,"kind":"params","status":"succeeded","created_by":"admin"}]`)
	code, out, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "ops", "ls", "--unit-id", "3")
	if code != 0 || !strings.Contains(out, "params") {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
	if f.requests[0] != "GET /api/v1/operations?unit_id=3" {
		t.Fatalf("request = %q", f.requests[0])
	}
}

func TestOpsListRejectsBadUnitFilter(t *testing.T) {
	srv, f := newFake(t)
	code, _, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "ops", "ls", "--unit-id", "3&x=1")
	if code != 2 || len(f.requests) != 0 {
		t.Fatalf("exit = %d, requests = %v", code, f.requests)
	}
}

func TestOpsGetRequestsSingleOperation(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/operations/9"] = jsonHandler(`{"id":9,"kind":"params","status":"failed","error":"apply failed"}`)
	code, out, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "ops", "get", "9")
	if code != 0 || !strings.Contains(out, "apply failed") {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
}

func TestStatusShowsDashboardSummary(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/dashboard"] = jsonHandler(`{"hosts":4,"hosts_online":3,"units":2,"instances":6,"problems":["主机 node-c 离线"]}`)
	code, out, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "status")
	if code != 0 || !strings.Contains(out, "3/4") || !strings.Contains(out, "node-c") {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
}

func TestJSONFlagPrintsRawResponse(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/units"] = jsonHandler(`[{"id":3,"name":"orders"}]`)
	code, out, _ := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "--json", "units", "ls")
	if code != 0 || strings.TrimSpace(out) != `[{"id":3,"name":"orders"}]` {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
}

// 写类子命令不在第一版范围内（需要 TOTP 二次校验），必须明确拒绝。
func TestMutatingCommandsAreNotAvailable(t *testing.T) {
	srv, f := newFake(t)
	for _, args := range [][]string{
		{"units", "restart", "3"},
		{"units", "delete", "3"},
		{"hosts", "revoke", "1"},
		{"backup", "3"},
		{"exec", "3", "FLUSHALL"},
	} {
		code, _, errb := runCLI(t, env(srv.URL), append([]string{"--ca-file", caFile(t, srv)}, args...)...)
		if code != 2 || !strings.Contains(errb, "未知") {
			t.Fatalf("%v: exit = %d, stderr = %q", args, code, errb)
		}
	}
	if len(f.requests) != 0 {
		t.Fatalf("mutating command sent requests: %v", f.requests)
	}
}

func TestServerErrorIsReportedWithoutToken(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/units"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"FORBIDDEN","message":"当前角色无权限"}`))
	}
	code, _, errb := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "units", "ls")
	if code != 1 || !strings.Contains(errb, "403") || !strings.Contains(errb, "FORBIDDEN") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
	if strings.Contains(errb, testToken) {
		t.Fatal("token leaked into error output")
	}
}

// 即使服务端在错误信息里回显了令牌，输出时也必须脱敏。
func TestEchoedTokenIsRedacted(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/units"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"UNAUTHORIZED","message":"bad token ` + testToken + `"}`))
	}
	code, _, errb := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "units", "ls")
	if code != 1 || strings.Contains(errb, testToken) || !strings.Contains(errb, "[REDACTED]") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
}

// 响应有大小上限，超限时中止并说明，不把超大内容打到终端。
func TestOversizedResponseIsAborted(t *testing.T) {
	srv, f := newFake(t)
	f.routes["/api/v1/units"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := bytes.Repeat([]byte("a"), 64*1024)
		for i := 0; i < 80; i++ { // 5 MiB > 4 MiB 上限
			_, _ = w.Write(chunk)
		}
	}
	code, out, errb := runCLI(t, env(srv.URL), "--ca-file", caFile(t, srv), "units", "ls")
	if code != 1 || !strings.Contains(errb, "超过") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
	if len(out) > 0 {
		t.Fatalf("partial output printed: %d bytes", len(out))
	}
}

func TestUnknownCommandExitsWithUsage(t *testing.T) {
	code, _, errb := runCLI(t, env("https://127.0.0.1:1"), "frobnicate")
	if code != 2 || !strings.Contains(errb, "用法") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
}

func TestMissingCAFileFails(t *testing.T) {
	code, _, errb := runCLI(t, env("https://127.0.0.1:1"), "--ca-file", "/nonexistent/ca.pem", "units", "ls")
	if code != 2 || !strings.Contains(errb, "ca-file") {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
}

// 不接受 shell 展开或 sh -c：包中不得出现 os/exec，本 CLI 只做 HTTP 调用。
func TestNoProcessExecution(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"os/exec", "sh -c", "exec.Command"} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("main.go contains %q", bad)
		}
	}
}
