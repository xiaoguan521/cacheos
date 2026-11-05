// Package web 内嵌控制台前端构建产物（web/dist），由 syapp-console 提供静态文件。
// 构建前端：cd web && pnpm build（产物输出到 dist/）。
package web

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
