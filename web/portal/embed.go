// Package portal 内嵌授权后台前端的构建产物（web/portal/dist），只由 syapp-license-server 使用，
// 不进入客户的控制台程序。构建：cd web && pnpm build:portal。
package portal

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist 返回 dist 目录的文件系统；未构建前端时只有占位文件。
func Dist() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}
