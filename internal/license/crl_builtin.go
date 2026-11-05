package license

import (
	_ "embed"
	"strings"
)

// builtin.crl 是随控制台版本发布的最新吊销列表（由离线签名机签名；发布前从授权后台导出放到这里）。
// 为空表示没有内置吊销列表。
//
//go:embed builtin.crl
var builtinCRL string

// BuiltinCRL 返回内置吊销列表；为空或验签失败时返回 nil。
func BuiltinCRL(keys KeyRing) *CRL {
	if strings.TrimSpace(builtinCRL) == "" {
		return nil
	}
	c, err := VerifyCRL(builtinCRL, keys)
	if err != nil {
		return nil
	}
	return c
}

// SplitBundle 把客户收到的授权文件拆成 License 与吊销列表（文件可以只有其中一种，也可以两者都有，每行一项）。
func SplitBundle(raw string) (lic string, crls []string) {
	for _, ln := range strings.Split(raw, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case ln == "" || strings.HasPrefix(ln, "#"):
		case strings.HasPrefix(ln, crlPrefix):
			crls = append(crls, ln)
		case lic == "":
			lic = ln
		}
	}
	return lic, crls
}
