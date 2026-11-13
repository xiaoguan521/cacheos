package instance

import (
	"fmt"
	"path/filepath"
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/webcli"
)

// 扩展数据类型模块（ADR 0002）。名称是白名单，同时决定加载顺序与文件名：<Root>/modules/<名称>.so。
// 控制台只下发名称；agent 自己拼出路径，不接收任何路径或文件名。
var moduleOrder = []string{"json", "search", "bloom"}

// 模块命令名前缀，用来从 Web 命令行拒绝清单里挑出属于某个模块的命令。
var modulePrefix = map[string]string{"json": "JSON.", "search": "FT.", "bloom": "BF."}

// ValidateModules 校验创建实例时选择的模块。模块只能在新建时选择，之后不可热加载（MODULE 命令保持禁用）。
func ValidateModules(mods []string, kind agentv1.Kind, cluster bool) error {
	if len(mods) == 0 {
		return nil
	}
	if kind != agentv1.Kind_KIND_SERVER {
		return fmt.Errorf("modules are only supported on server instances")
	}
	seen := map[string]bool{}
	for _, m := range mods {
		if _, ok := modulePrefix[m]; !ok {
			return fmt.Errorf("module %q is not allowed", m)
		}
		if seen[m] {
			return fmt.Errorf("module %q listed twice", m)
		}
		seen[m] = true
	}
	if seen["search"] && cluster {
		return fmt.Errorf("module search is not supported in cluster mode")
	}
	return nil
}

// ModulePath 返回模块文件路径；name 必须已通过 ValidateModules。
func (l Layout) ModulePath(name string) string {
	return filepath.Join(l.Root, "modules", name+".so")
}

// ModuleConfLines 渲染 loadmodule 配置行，顺序固定为 json → search → bloom。
func (l Layout) ModuleConfLines(mods []string) string {
	var b strings.Builder
	for _, name := range moduleOrder {
		for _, m := range mods {
			if m == name {
				fmt.Fprintf(&b, "loadmodule %s\n", l.ModulePath(name))
			}
		}
	}
	return b.String()
}

// WebCLIRulesFor 返回 Web 命令行账号在已加载模块下的规则：静态规则 + 拒绝清单中属于这些模块的命令。
// 模块没加载时这些命令不存在，ACL SETUSER 会因未知命令失败，所以只能按已加载模块追加。
func WebCLIRulesFor(mods []string) []string {
	rules := append([]string(nil), WebCLIRules...)
	for _, name := range moduleOrder {
		loaded := false
		for _, m := range mods {
			loaded = loaded || m == name
		}
		if !loaded {
			continue
		}
		for _, c := range webcli.DeniedCommands {
			if strings.HasPrefix(c, modulePrefix[name]) {
				rules = append(rules, "-"+strings.ToLower(c))
			}
		}
	}
	return rules
}
