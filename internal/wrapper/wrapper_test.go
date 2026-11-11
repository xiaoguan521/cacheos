package wrapper

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// testRoot 搭一个最小的 /home/atwasoft/sycache：实例 7001（server）、26379（sentinel）、版本 9.1.3 与 9.1.4。
func testRoot(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch := func(p string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("instances/7001")
	touch("instances/7001/sycache.conf")
	mk("instances/26379")
	touch("instances/26379/sycache-sentinel.conf")
	mk("instances/7002") // 目录存在但没有配置
	for _, v := range []string{"9.1.3", "9.1.4-hf.1"} {
		mk("versions/" + v + "/bin")
		touch("versions/" + v + "/bin/sycache-server")
	}
	mk("versions/9.2.0") // 没有二进制
	if err := os.Symlink("versions/9.1.3", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	return Paths{Root: root, Systemctl: "/usr/bin/systemctl"}
}

func TestBuildLifecycle(t *testing.T) {
	p := testRoot(t)
	for argv, want := range map[[2]string][]string{
		{"start", "7001"}:    {"start", "sycache@7001.service"},
		{"stop", "7001"}:     {"stop", "sycache@7001.service"},
		{"restart", "7001"}:  {"restart", "sycache@7001.service"},
		{"start", "26379"}:   {"start", "sycache-sentinel@26379.service"},
		{"restart", "26379"}: {"restart", "sycache-sentinel@26379.service"},
	} {
		cmd, err := p.Build(argv[:])
		if err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		if cmd.Path != "/usr/bin/systemctl" || !reflect.DeepEqual(cmd.Args, want) || !reflect.DeepEqual(cmd.Env, SafeEnv) {
			t.Fatalf("%v: %+v", argv, cmd)
		}
	}
}

func TestBuildSwitchVersion(t *testing.T) {
	p := testRoot(t)
	for _, v := range []string{"9.1.3", "9.1.4-hf.1"} {
		cmd, err := p.Build([]string{"switch-version", v})
		if err != nil || cmd.Switch != v || cmd.Path != "" {
			t.Fatalf("%s: %+v %v", v, cmd, err)
		}
		if err := p.SwitchVersion(cmd.Switch); err != nil {
			t.Fatal(err)
		}
		got, _ := os.Readlink(filepath.Join(p.Root, "current"))
		if got != "versions/"+v {
			t.Fatalf("current -> %q", got)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Root, "current.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary link left behind")
	}
}

// 每个子命令的注入用例：任何不合规输入都必须返回 ErrUsage，且不产生可执行命令。
func TestBuildRejectsInjection(t *testing.T) {
	p := testRoot(t)
	ports := []string{
		"", "7", "700", "123456", "70O1", "7001 ", " 7001", "7001\n", "7001;id", "7001 && id", "$(id)", "`id`",
		"../7001", "7001/../../etc", "-7001", "+7001", "0x1b59", "7001\x00", "７００１", "0999", "65536", "99999",
		"7002", // 目录存在但没有配置
		"7003", // 目录不存在
		"--now", "-h", "*", "7001,7002",
	}
	for _, sub := range []string{"start", "stop", "restart"} {
		for _, port := range ports {
			if cmd, err := p.Build([]string{sub, port}); !errors.Is(err, ErrUsage) {
				t.Errorf("%s %q accepted: %+v", sub, port, cmd)
			}
		}
		for _, argv := range [][]string{{sub}, {sub, "7001", "7001"}, {sub, "7001", "--force"}} {
			if _, err := p.Build(argv); !errors.Is(err, ErrUsage) {
				t.Errorf("%q accepted", argv)
			}
		}
	}
	versions := []string{
		"", "9", "9.1", "9.1.3.4", "v9.1.3", "9.1.3-", "9.1.3-HF", "9.1.3-hf_1", "9.1.3-hf/1", "9.1.3/../../..",
		"../9.1.3", "9.1.3;id", "9.1.3 ", "9.1.3\n", "$(id)", "9.1.3-hf.1 x",
		"9.2.0", // 目录存在但没有二进制
		"9.9.9", // 目录不存在
	}
	for _, v := range versions {
		if cmd, err := p.Build([]string{"switch-version", v}); !errors.Is(err, ErrUsage) {
			t.Errorf("switch-version %q accepted: %+v", v, cmd)
		}
		if err := p.SwitchVersion(v); err == nil {
			t.Errorf("SwitchVersion(%q) succeeded", v)
		}
	}
	for _, argv := range [][]string{
		nil, {}, {"switch-version"}, {"switch-version", "9.1.3", "9.1.4-hf.1"},
		{"reload"}, {"exec", "id"}, {"status", "7001"}, {"START", "7001"}, {"start;id", "7001"}, {"-c", "start 7001"},
		{"enable", "7001"}, {"disable", "7001"}, {"kill", "7001"}, {"daemon-reload"},
	} {
		if _, err := p.Build(argv); !errors.Is(err, ErrUsage) {
			t.Errorf("%q accepted", argv)
		}
	}
}

// 实例目录或版本目录是指向根目录之外的软链时拒绝（防止借 sycache 用户可写目录绕出）。
func TestBuildRejectsSymlinkEscape(t *testing.T) {
	p := testRoot(t)
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "sycache.conf"), nil, 0o644)
	_ = os.MkdirAll(filepath.Join(outside, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(outside, "bin", "sycache-server"), nil, 0o755)
	if err := os.Symlink(outside, filepath.Join(p.Root, "instances", "7009")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.Root, "versions", "6.6.6")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Build([]string{"start", "7009"}); !errors.Is(err, ErrUsage) {
		t.Error("symlinked instance dir accepted")
	}
	if _, err := p.Build([]string{"switch-version", "6.6.6"}); !errors.Is(err, ErrUsage) {
		t.Error("symlinked version dir outside root accepted")
	}
	// 安装包自带的「基础版本」软链（versions/<版本> -> ..）在根目录之内，允许
	if err := os.Symlink("..", filepath.Join(p.Root, "versions", "9.0.0")); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(p.Root, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(p.Root, "bin", "sycache-server"), nil, 0o755)
	if _, err := p.Build([]string{"switch-version", "9.0.0"}); err != nil {
		t.Errorf("in-root base version rejected: %v", err)
	}
}
