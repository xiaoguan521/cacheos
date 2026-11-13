package instance

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// 主机上报的“已安装模块”只看 current 指向的版本目录里 modules/<名>.so 的普通文件，按规范顺序，名称必须在白名单内。
func TestCurrentModules(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "versions", "9.1.3", "modules")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"bloom.so", "json.so", "other.so", "search.so.bak", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink("/etc/passwd", filepath.Join(d, "search.so")) // 软链不算
	if err := os.Symlink("versions/9.1.3", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	l := Layout{Root: root}
	if got := l.CurrentModules(); !reflect.DeepEqual(got, []string{"json", "bloom"}) {
		t.Errorf("CurrentModules = %v, want [json bloom]", got)
	}
	if got := (Layout{Root: t.TempDir()}).CurrentModules(); got != nil {
		t.Errorf("no current version must report no modules, got %v", got)
	}
}
