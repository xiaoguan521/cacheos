package instance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/webcli"
)

func TestValidateModules(t *testing.T) {
	srv, sen := agentv1.Kind_KIND_SERVER, agentv1.Kind_KIND_SENTINEL
	ok := []struct {
		mods    []string
		kind    agentv1.Kind
		cluster bool
	}{
		{nil, srv, false},
		{[]string{"json"}, srv, false},
		{[]string{"json", "search", "bloom"}, srv, false},
		{[]string{"json", "bloom"}, srv, true},
	}
	for _, c := range ok {
		if err := ValidateModules(c.mods, c.kind, c.cluster); err != nil {
			t.Errorf("%v: unexpected error %v", c.mods, err)
		}
	}
	bad := []struct {
		name    string
		mods    []string
		kind    agentv1.Kind
		cluster bool
	}{
		{"unknown", []string{"graph"}, srv, false},
		{"path injection", []string{"../../etc/x"}, srv, false},
		{"file name with suffix", []string{"json.so"}, srv, false},
		{"empty name", []string{""}, srv, false},
		{"duplicate", []string{"json", "json"}, srv, false},
		{"search in cluster", []string{"search"}, srv, true},
		{"module on sentinel", []string{"json"}, sen, false},
	}
	for _, c := range bad {
		if err := ValidateModules(c.mods, c.kind, c.cluster); err == nil {
			t.Errorf("%s: accepted %v", c.name, c.mods)
		}
	}
}

func TestModuleConfLines(t *testing.T) {
	l := Layout{Root: "/r"}
	got := l.ModuleConfLines([]string{"bloom", "json"}) // 顺序归一：json → search → bloom
	want := "loadmodule /r/current/modules/json.so\nloadmodule /r/current/modules/bloom.so\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if l.ModuleConfLines(nil) != "" {
		t.Error("no modules must render nothing")
	}
}

func TestServerConfRendersModules(t *testing.T) {
	l := Layout{Root: "/r", RunDir: "/run", LogDir: "/log"}
	st := &State{Port: 6400, Modules: []string{"json"}}
	conf := l.ServerConf(st, &agentv1.CreateInstance{})
	if !strings.Contains(conf, "loadmodule /r/current/modules/json.so\n") {
		t.Errorf("conf lacks loadmodule:\n%s", conf)
	}
	if !strings.Contains(conf, "enable-module-command no") {
		t.Error("MODULE command must stay disabled")
	}
	if strings.Contains(l.ServerConf(&State{Port: 6400}, &agentv1.CreateInstance{}), "loadmodule") {
		t.Error("modules are off by default")
	}
}

// 加载了模块的实例，webcli 账号要在 ACL 层就拒绝拒绝清单里属于该模块的命令（不止靠 agent 校验）。
func TestWebCLIRulesForModules(t *testing.T) {
	if got := WebCLIRulesFor(nil); strings.Join(got, " ") != strings.Join(WebCLIRules, " ") {
		t.Errorf("no modules must keep the static rules: %v", got)
	}
	for mod, prefix := range map[string]string{"json": "JSON.", "bloom": "BF.", "search": "FT."} {
		rules := strings.Join(WebCLIRulesFor([]string{mod}), " ")
		n := 0
		for _, c := range webcli.DeniedCommands {
			if strings.HasPrefix(c, prefix) {
				n++
				if !strings.Contains(" "+rules+" ", " -"+strings.ToLower(c)+" ") {
					t.Errorf("%s: rules lack -%s", mod, strings.ToLower(c))
				}
			}
		}
		if n == 0 {
			t.Errorf("%s: denylist has no command with prefix %s", mod, prefix)
		}
	}
	// 没加载 search 就不能出现 FT.* 规则，否则 ACL SETUSER 会因未知命令失败
	if strings.Contains(strings.Join(WebCLIRulesFor([]string{"json"}), " "), "-ft.") {
		t.Error("rules for json must not mention ft.* commands")
	}
}

func TestACLFileWebCLIModuleRules(t *testing.T) {
	st := &State{AdminPassword: "a", WebCLIPassword: "w", Modules: []string{"bloom"}}
	acl := ACLFile(st, "", nil)
	if !strings.Contains(acl, "-bf.load") {
		t.Errorf("webcli account lacks -bf.load:\n%s", acl)
	}
	if strings.Contains(ACLFile(&State{AdminPassword: "a", WebCLIPassword: "w"}, "", nil), "-bf.load") {
		t.Error("bf.load rule must not appear without the module")
	}
}

// 业务账号即使选了 +@all，也不能执行已加载模块里的调试 / 内部 / 整体载入命令：拒绝规则追加在用户规则之后。
func TestUserArgsForDeniesModuleCommands(t *testing.T) {
	u := &agentv1.AclUser{Name: "app", Enabled: true, Rules: []string{"~*", "+@all"}}
	args := strings.Join(UserArgsFor(u, []string{"json", "bloom"}), " ")
	for _, want := range []string{"-json.debug", "-bf.load"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %s: %s", want, args)
		}
	}
	if strings.Index(args, "-json.debug") < strings.Index(args, "+@all") {
		t.Error("deny rules must come after the user's own rules")
	}
	if strings.Contains(args, "-ft.") {
		t.Error("search is not loaded, no ft.* rules allowed")
	}
	if got := strings.Join(UserArgsFor(u, nil), " "); got != strings.Join(UserArgs(u), " ") {
		t.Errorf("no modules must equal UserArgs: %s", got)
	}
}

func TestACLFileBusinessUserModuleRules(t *testing.T) {
	st := &State{AdminPassword: "a", Modules: []string{"json"}}
	u := &agentv1.AclUser{Name: "app", Enabled: true, PasswordSha256: []string{strings.Repeat("a", 64)}, Rules: []string{"~*", "+@all"}}
	if acl := ACLFile(st, "", []*agentv1.AclUser{u}); !strings.Contains(acl, "user app ") || !strings.Contains(acl, "-json.debug") {
		t.Errorf("business user lacks module deny rules:\n%s", acl)
	}
}

func TestModulePathIsVersioned(t *testing.T) {
	l := Layout{Root: "/r"}
	if got := l.ModulePath("json"); got != "/r/current/modules/json.so" {
		t.Errorf("ModulePath = %s; modules must follow the current version", got)
	}
}

func TestRequiredAndMissingModules(t *testing.T) {
	m := newWebCLITestManager(t, 7001, &State{Port: 7001, Modules: []string{"json", "bloom"}})
	writeState := func(port uint32, st *State) {
		b, _ := json.Marshal(st)
		if err := os.WriteFile(filepath.Join(m.L.StateDir, strconv.Itoa(int(port))+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeState(7002, &State{Port: 7002, Modules: []string{"json", "search"}})
	writeState(7003, &State{Port: 7003}) // 没有模块
	if got := m.RequiredModules(); !reflect.DeepEqual(got, []string{"json", "search", "bloom"}) {
		t.Errorf("RequiredModules = %v", got)
	}
	have := func(n string) bool { return n == "json" || n == "bloom" }
	if got := MissingModules([]string{"json", "search", "bloom"}, have); !reflect.DeepEqual(got, []string{"search"}) {
		t.Errorf("MissingModules = %v", got)
	}
	if MissingModules(nil, have) != nil {
		t.Error("nothing required, nothing missing")
	}
}

func TestInstalledModule(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "versions", "9.1.4", "modules")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(d, "json.so"), []byte("x"), 0o755)
	_ = os.Symlink("/etc/passwd", filepath.Join(d, "bloom.so")) // 软链不算已安装
	l := Layout{Root: root}
	if !l.InstalledModule("9.1.4", "json") {
		t.Error("json.so is installed")
	}
	if l.InstalledModule("9.1.4", "bloom") || l.InstalledModule("9.1.4", "search") || l.InstalledModule("9.1.5", "json") {
		t.Error("symlink or missing file must not count")
	}
}
