package instance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
)

// Create 对模块的校验都发生在写任何文件、启动任何进程之前：被拒绝时实例目录与状态都不能留下。
func TestCreateRejectsBadModules(t *testing.T) {
	cases := []struct {
		name string
		c    *agentv1.CreateInstance
	}{
		{"unknown", &agentv1.CreateInstance{Port: 16401, Modules: []string{"graph"}}},
		{"path", &agentv1.CreateInstance{Port: 16402, Modules: []string{"../x"}}},
		{"sentinel", &agentv1.CreateInstance{Port: 16403, Kind: agentv1.Kind_KIND_SENTINEL, Modules: []string{"json"}}},
		{"search in cluster", &agentv1.CreateInstance{Port: 16404, ClusterEnabled: true, Modules: []string{"search"}}},
		{"file missing", &agentv1.CreateInstance{Port: 16405, Modules: []string{"json"}}},
	}
	for _, c := range cases {
		m := newWebCLITestManager(t, c.c.Port, nil)
		_, err := m.Create(context.Background(), c.c)
		if errCode(err) != "INVALID" {
			t.Errorf("%s: err = %v, want INVALID", c.name, err)
		}
		if _, e := os.Lstat(m.L.InstanceDir(c.c.Port)); e == nil {
			t.Errorf("%s: instance dir left behind", c.name)
		}
	}
}

// 模块文件存在、校验通过时，配置里有 loadmodule，状态里记下模块；wrapper 用假的以避免真正启动实例。
func TestCreateRecordsAndRendersModules(t *testing.T) {
	const port = 16410
	m := newWebCLITestManager(t, port, nil)
	for _, d := range []string{"modules", "instances"} { // 生产环境由安装包创建这两个目录
		if err := os.MkdirAll(filepath.Join(m.L.Root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(m.L.ModulePath("json"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Run = func(ctx context.Context, args ...string) (string, error) { return "", errStop }
	_, err := m.Create(context.Background(), &agentv1.CreateInstance{Port: port, Modules: []string{"json"}})
	if errCode(err) != "INTERNAL" { // 校验通过，走到了（假的）启动步骤
		t.Fatalf("err = %v, want failure at the start step", err)
	}
	st, err := m.Load(port)
	if err != nil || len(st.Modules) != 1 || st.Modules[0] != "json" {
		t.Fatalf("state modules not recorded: %+v %v", st, err)
	}
	conf, err := os.ReadFile(m.L.ConfPath(port, agentv1.Kind_KIND_SERVER))
	if err != nil || !strings.Contains(string(conf), "loadmodule "+m.L.ModulePath("json")+"\n") {
		t.Errorf("conf lacks loadmodule: %v\n%s", err, conf)
	}
	acl, _ := os.ReadFile(m.L.ACLPath(port))
	if !strings.Contains(string(acl), "-json.debug") {
		t.Errorf("acl lacks module deny rules:\n%s", acl)
	}
}

type stopErr struct{}

func (stopErr) Error() string { return "stop" }

var errStop error = stopErr{}
