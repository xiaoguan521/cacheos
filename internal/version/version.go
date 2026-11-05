// Package version 保存构建期注入的版本信息。
//
// 构建时通过 -ldflags 注入，例如：
//
//	go build -ldflags "-X github.com/xiaoguan521/cacheos/internal/version.Version=1.0.0 \
//	  -X github.com/xiaoguan521/cacheos/internal/version.BuildDate=2026-10-27"
//
// BuildDate 同时用于 License 的 maintenance_until 校验（规格书 8.3）。
package version

import "fmt"

var (
	// Product 是对外品牌名。
	Product = "CacheOS"
	// Version 是语义化版本号。
	Version = "0.0.0-dev"
	// Commit 是 git 提交哈希。
	Commit = "unknown"
	// BuildDate 是 UTC 构建日期，格式 YYYY-MM-DD。
	BuildDate = "1970-01-01"
)

// String 返回一行版本信息。
func String(component string) string {
	return fmt.Sprintf("%s %s %s (commit %s, built %s)", Product, component, Version, Commit, BuildDate)
}
