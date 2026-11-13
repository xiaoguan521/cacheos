package orch

import (
	"reflect"
	"testing"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func TestNormalizeUnitModules(t *testing.T) {
	good := []struct {
		mode string
		in   []string
		want []string
	}{
		{store.ModeStandalone, nil, nil},
		{store.ModeStandalone, []string{"bloom", "json"}, []string{"json", "bloom"}}, // 顺序归一
		{store.ModeReplication, []string{"search"}, []string{"search"}},
		{store.ModeSentinel, []string{"json"}, []string{"json"}}, // 哨兵单元：只有数据实例加载
		{store.ModeCluster, []string{"json", "bloom"}, []string{"json", "bloom"}},
	}
	for _, c := range good {
		got, err := NormalizeUnitModules(c.mode, c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v: got %v, %v; want %v", c.mode, c.in, got, err, c.want)
		}
	}
	bad := []struct {
		mode string
		in   []string
	}{
		{store.ModeStandalone, []string{"graph"}},
		{store.ModeStandalone, []string{"json", "json"}},
		{store.ModeStandalone, []string{"../json"}},
		{store.ModeCluster, []string{"search"}},
	}
	for _, c := range bad {
		if _, err := NormalizeUnitModules(c.mode, c.in); err == nil {
			t.Errorf("%s %v accepted", c.mode, c.in)
		}
	}
}

func TestUnitModuleListAndRequest(t *testing.T) {
	u := &store.Unit{Modules: "json,bloom"}
	if got := UnitModules(u); !reflect.DeepEqual(got, []string{"json", "bloom"}) {
		t.Errorf("UnitModules = %v", got)
	}
	if UnitModules(&store.Unit{}) != nil {
		t.Error("empty unit must have no modules")
	}
	// 只有数据实例带模块；哨兵进程不带
	if got := requestModules(u, agentv1.Kind_KIND_SERVER); !reflect.DeepEqual(got, []string{"json", "bloom"}) {
		t.Errorf("server modules = %v", got)
	}
	if got := requestModules(u, agentv1.Kind_KIND_SENTINEL); got != nil {
		t.Errorf("sentinel must not get modules: %v", got)
	}
}
