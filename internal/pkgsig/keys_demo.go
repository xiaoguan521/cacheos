//go:build !release

package pkgsig

// demoKeys 是演示 / 开发构建内置的升级包签名公钥（私钥在 demo/keys/，不提交）。v* 正式版本用 -tags release 构建，不含它。
var demoKeys = map[string]string{
	"cacheos-demo-pkg-2026": "chLiVFnNz10CvyCDxPrIk7otYTq3ZhS+gCnzjUdPA98=",
}
