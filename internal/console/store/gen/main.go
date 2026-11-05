// gen 用 gorm/gen 从 store 的模型生成类型安全的查询代码（internal/console/store/query）。
//
// 用法（在仓库根目录）：go generate ./internal/console/store/
// 生成结果提交到仓库；CI 会重新生成并检查没有差异。
package main

import (
	"flag"
	"log"

	"gorm.io/gen"

	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func main() {
	out := flag.String("out", "query", "输出目录")
	flag.Parse()
	g := gen.NewGenerator(gen.Config{
		OutPath: *out,
		// 不生成全局变量：调用方用 query.Use(db) 拿到查询对象，便于测试与事务
		Mode: gen.WithQueryInterface,
	})
	g.ApplyBasic(store.Models()...)
	g.Execute()
	log.Printf("generated %d models into %s", len(store.Models()), *out)
}
