package store_test

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// CLAUDE.md：数据库访问只用参数化查询，正式版统一使用 gorm/gen 生成的类型安全查询。
//
// 本测试用类型信息扫描全部非测试代码：在数据层之外调用 *gorm.DB 的查询方法（Where、Order、Raw、Exec、Find、Create……）
// 一律视为违规——这些方法接受字符串条件，容易写出拼接 SQL。业务代码改用 query.Use(db) 生成的查询。
//
// 允许的位置：
//   - internal/console/store/...      数据层本身（模型、迁移、生成代码、测试库）
//   - internal/console/backup         逐表导出 / 导入，按模型反射泛型处理所有表，无法用生成查询表达
//   - internal/portal/store/...       授权后台的数据层（同样只有模型、迁移、生成代码、测试库）
var allowedPkgs = []string{
	"github.com/xiaoguan521/cacheos/internal/console/store",
	"github.com/xiaoguan521/cacheos/internal/portal/store",
	"github.com/xiaoguan521/cacheos/internal/console/backup",
}

var queryMethods = map[string]bool{
	"Where": true, "Or": true, "Not": true, "Order": true, "Group": true, "Having": true, "Joins": true,
	"Select": true, "Omit": true, "Raw": true, "Exec": true, "Model": true, "Table": true, "Distinct": true,
	"Find": true, "First": true, "Last": true, "Take": true, "Scan": true, "Pluck": true, "Count": true,
	"Create": true, "CreateInBatches": true, "Save": true, "Update": true, "Updates": true, "UpdateColumn": true,
	"UpdateColumns": true, "Delete": true, "FirstOrCreate": true, "FirstOrInit": true, "Preload": true,
	"Limit": true, "Offset": true, "Clauses": true, "Assign": true, "Attrs": true, "Unscoped": true,
}

func allowed(pkg string) bool {
	for _, a := range allowedPkgs {
		if pkg == a || strings.HasPrefix(pkg, a+"/") {
			return true
		}
	}
	return false
}

func TestNoRawGormOutsideStore(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir: root, Tests: false}
	pkgs, err := packages.Load(cfg, "./cmd/...", "./internal/...")
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			t.Fatalf("load %s: %v", p.PkgPath, p.Errors)
		}
		if allowed(p.PkgPath) {
			continue
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !queryMethods[sel.Sel.Name] {
					return true
				}
				tv, ok := p.TypesInfo.Types[sel.X]
				if !ok {
					return true
				}
				if ptr, ok := tv.Type.(*types.Pointer); ok {
					if named, ok := ptr.Elem().(*types.Named); ok && named.Obj().Pkg() != nil &&
						named.Obj().Pkg().Path() == "gorm.io/gorm" && named.Obj().Name() == "DB" {
						pos := p.Fset.Position(call.Pos())
						rel, _ := filepath.Rel(root, pos.Filename)
						bad = append(bad, rel+":"+strconv.Itoa(pos.Line)+" (*gorm.DB)."+sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("%d 处在数据层之外直接使用 *gorm.DB 查询方法，请改用 query.Use(db) 生成的类型安全查询：\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}
