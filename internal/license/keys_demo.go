//go:build !release

package license

// 演示密钥 etos-demo-2026：只用于 dev-* 演示构建与本地开发。
// 正式版本用 -tags release 构建，不包含这把密钥（规格书 8.8.1）。
func init() {
	builtinKeys[DemoKeyID] = "2s66YtpjpEGRSzQU7Sus8hvAbDXp6yAQjOI3y1AeJlU="
}
