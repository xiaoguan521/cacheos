package instance

import (
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
		{"upstream file name", []string{"valkey-json"}, srv, false},
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
	want := "loadmodule /r/modules/json.so\nloadmodule /r/modules/bloom.so\n"
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
	if !strings.Contains(conf, "loadmodule /r/modules/json.so\n") {
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
