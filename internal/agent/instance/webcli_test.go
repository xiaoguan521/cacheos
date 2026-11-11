package instance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/resp"
	"github.com/xiaoguan521/cacheos/internal/webcli"
)

// 改动前现行的 ACL 规则逐字快照（CLAUDE.md：管理账号与 reader 账号规则不得因 Web 命令行而改动）。
const wantAgentRules = "~* &* -@all +ping +info +role +dbsize +lastsave +auth +hello +config|get +config|set +config|rewrite +config|resetstat +replicaof +failover +cluster +migrate +readonly +slowlog +latency +commandlog +memory|stats +memory|doctor +memory|usage +memory|malloc-stats +client|list +client|kill +client|setname +client|info +acl|setuser +acl|deluser +acl|list +acl|users +acl|save +acl|whoami +bgsave +bgrewriteaof +shutdown +scan +type +ttl +pttl +object|encoding +object|freq +object|idletime +restore +del"
const wantReaderRules = "~* &* -@all +type +ttl +pttl +memory|usage +get +hget +hgetall +lrange +smembers +zrange +xrange"

// Web 命令行账号的规则（CLAUDE.md 指定的字符串，逐字）。
const wantWebCLIRules = "~* resetchannels +@all -@admin -@dangerous -@scripting -@blocking -@pubsub -@transaction -@connection +info +dbsize"

func TestManagementAndReaderRulesUnchanged(t *testing.T) {
	if got := strings.Join(AgentRules, " "); got != wantAgentRules {
		t.Fatalf("agent rules changed:\n%s", got)
	}
	if got := strings.Join(ReaderRules, " "); got != wantReaderRules {
		t.Fatalf("reader rules changed:\n%s", got)
	}
	st := &State{AdminPassword: "admin-pass", ReadPassword: "read-pass"}
	acl := ACLFile(st, "", nil)
	for _, want := range []string{
		"user sycache-ops on #" + sha("admin-pass") + " " + wantAgentRules + "\n",
		"user sycache-reader on #" + sha("read-pass") + " " + wantReaderRules + "\n",
	} {
		if !strings.Contains(acl, want) {
			t.Errorf("users.acl lost %q", want)
		}
	}
}

func TestWebCLIRulesString(t *testing.T) {
	if got := strings.Join(WebCLIRules, " "); got != wantWebCLIRules {
		t.Fatalf("web cli rules = %q", got)
	}
	if UserWebCLI != "sycache-webcli" {
		t.Fatalf("user name = %q", UserWebCLI)
	}
}

func TestWebCLIAccountRendered(t *testing.T) {
	st := &State{AdminPassword: "admin-pass", ReadPassword: "read-pass", WebCLIPassword: "webcli-pass"}
	acl := ACLFile(st, "repl", nil)
	want := "user sycache-webcli on #" + sha("webcli-pass") + " " + wantWebCLIRules + "\n"
	if !strings.Contains(acl, want) {
		t.Fatalf("users.acl lacks web cli account:\n%s", acl)
	}
	// 口令只以哈希出现
	if strings.Contains(acl, "webcli-pass") {
		t.Fatal("plaintext web cli password in users.acl")
	}
	// 账号不得出现在管理账号或 reader 的规则里
	if strings.Count(acl, "sycache-webcli") != 1 {
		t.Fatalf("web cli account rendered more than once:\n%s", acl)
	}
}

func TestWebCLIAccountAbsentForLegacyInstance(t *testing.T) {
	// 没有 WebCLIPassword 的存量实例：不渲染该账号，ExecCommand 返回 UNSUPPORTED。
	st := &State{AdminPassword: "admin-pass", ReadPassword: "read-pass"}
	if strings.Contains(ACLFile(st, "", nil), UserWebCLI) {
		t.Fatal("legacy instance must not get the web cli account on its own")
	}
}

func TestWebCLIAccountNameIsReserved(t *testing.T) {
	if err := ValidateUser(&agentv1.AclUser{Name: "sycache-webcli", PasswordSha256: []string{strings.Repeat("a", 64)}, Enabled: true}); err == nil {
		t.Fatal("business users must not use the reserved web cli name")
	}
}

// 拒绝清单与参数校验在连接实例之前完成：没有 socket、也没有口令时仍应返回 CLI_COMMAND_DENIED。
func newWebCLITestManager(t *testing.T, port uint32, st *State) *Manager {
	t.Helper()
	dir := t.TempDir()
	l := Layout{Root: dir + "/opt", RunDir: dir + "/run", LogDir: dir + "/log", StateDir: dir + "/state"}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if st != nil {
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(l.StateDir, strconv.Itoa(int(port))+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Manager{L: l}
}

func errCode(err error) string {
	if ie, ok := err.(*Error); ok {
		return ie.Code
	}
	return ""
}

func TestExecCommandRejectsBeforeConnecting(t *testing.T) {
	const port = 7001
	st := &State{Port: port, AdminPassword: "a", WebCLIPassword: "w"}
	m := newWebCLITestManager(t, port, st)
	ctx := context.Background()

	_, _, err := m.ExecCommand(ctx, &agentv1.ExecCommand{Port: port, Argv: []string{"FLUSHALL"}})
	if errCode(err) != webcli.CodeDenied {
		t.Fatalf("FLUSHALL: %v", err)
	}
	for _, argv := range [][]string{{"config", "get", "*"}, {"Client", "list"}, {"XREAD", "BLOCK", "0", "STREAMS", "s", "$"}} {
		if _, _, err := m.ExecCommand(ctx, &agentv1.ExecCommand{Port: port, Argv: argv}); errCode(err) != webcli.CodeDenied {
			t.Errorf("%v: %v", argv, err)
		}
	}
	if _, _, err := m.ExecCommand(ctx, &agentv1.ExecCommand{Port: port, Argv: []string{"GE T"}}); errCode(err) != webcli.CodeInvalid {
		t.Errorf("invalid name: %v", err)
	}
	// 不存在 / 非本 agent 管理的实例
	if _, _, err := m.ExecCommand(ctx, &agentv1.ExecCommand{Port: 7002, Argv: []string{"GET", "k"}}); errCode(err) != "NOT_FOUND" {
		t.Errorf("unmanaged port: %v", err)
	}
	// 存量实例（没有 WebCLIPassword）：合法命令返回 UNSUPPORTED，且不尝试连接
	legacy := newWebCLITestManager(t, port, &State{Port: port, AdminPassword: "a"})
	if _, _, err := legacy.ExecCommand(ctx, &agentv1.ExecCommand{Port: port, Argv: []string{"GET", "k"}}); errCode(err) != "UNSUPPORTED" {
		t.Errorf("legacy instance: %v", err)
	}
}

// 引擎侧 ACL 必须独立拒绝：用专用账号直连，绕过 agent 清单。需要 SYCACHE_BIN（与其他集成测试相同的约定）。
func TestIntegrationWebCLIAccount(t *testing.T) {
	bin := os.Getenv("SYCACHE_BIN")
	if bin == "" {
		t.Skip("SYCACHE_BIN not set")
	}
	base := t.TempDir()
	l := Layout{Root: base + "/opt", RunDir: base + "/run", LogDir: base + "/log", StateDir: base + "/state"}
	for _, d := range []string{l.Root + "/instances", l.RunDir, l.LogDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	m := &Manager{L: l}
	m.Run = fakeWrapper(t, bin, l)
	ctx := context.Background()
	port := freePort(t)
	t.Cleanup(func() { _, _ = m.Run(ctx, "stop", strconv.Itoa(int(port))) })
	if _, err := m.Create(ctx, &agentv1.CreateInstance{Port: port, Params: map[string]string{"save": ""}, RevisionId: "p00000001"}); err != nil {
		t.Fatal(err)
	}
	st, err := m.Load(port)
	if err != nil {
		t.Fatal(err)
	}
	if st.WebCLIPassword == "" {
		t.Fatal("new instance has no web cli password")
	}

	// 经 agent 的正常路径：数据命令成功
	for _, argv := range [][]string{{"SET", "k", "v"}, {"GET", "k"}} {
		payload, _, err := m.ExecCommand(ctx, &agentv1.ExecCommand{Port: port, Argv: argv})
		if err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		if strings.Contains(string(payload), `"truncated":true`) {
			t.Fatalf("unexpected truncation: %s", payload)
		}
	}
	if _, _, err := m.ExecCommand(ctx, &agentv1.ExecCommand{Port: port, Argv: []string{"CONFIG", "GET", "maxmemory"}}); errCode(err) != webcli.CodeDenied {
		t.Fatalf("agent must deny CONFIG before engine: %v", err)
	}

	// 直连专用账号：引擎以 NOPERM 拒绝（ACL 本身生效）
	c, err := resp.Dial(ctx, l.Socket(port), UserWebCLI, st.WebCLIPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, argv := range [][]string{{"CONFIG", "GET", "maxmemory"}, {"FLUSHALL"}, {"KEYS", "*"}, {"SUBSCRIBE", "ch"}, {"CLIENT", "LIST"}} {
		if _, err := c.Do(ctx, argv...); !resp.IsError(err, "NOPERM") {
			t.Errorf("engine accepted %v for web cli account: %v", argv, err)
		}
	}
}
