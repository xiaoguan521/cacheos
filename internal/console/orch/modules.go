package orch

import (
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/console/store"
)

// 扩展数据类型模块（ADR 0002）的规范顺序；与 agent 渲染 loadmodule 的顺序一致。
var unitModuleOrder = []string{"json", "search", "bloom"}

// NormalizeUnitModules 校验创建单元时选择的模块并返回规范顺序的列表。
// 校验规则与 agent 的 ValidateModules 相同（agent 创建实例时会再校验一次）：
// 只有白名单名称、不得重复、search 不支持集群。哨兵单元里只有数据实例加载模块。
func NormalizeUnitModules(mode string, mods []string) ([]string, error) {
	if len(mods) == 0 {
		return nil, nil
	}
	if err := instance.ValidateModules(mods, agentv1.Kind_KIND_SERVER, mode == store.ModeCluster); err != nil {
		return nil, invalid("%v", err)
	}
	var out []string
	for _, name := range unitModuleOrder {
		for _, m := range mods {
			if m == name {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

// UnitModules 返回单元已启用的模块（规范顺序）；没有时为 nil。
func UnitModules(u *store.Unit) []string {
	var out []string
	for _, m := range strings.Split(u.Modules, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// requestModules 是下发给某个进程的模块列表：只有数据实例加载模块，哨兵进程不加载。
func requestModules(u *store.Unit, kind agentv1.Kind) []string {
	if kind != agentv1.Kind_KIND_SERVER {
		return nil
	}
	return UnitModules(u)
}
