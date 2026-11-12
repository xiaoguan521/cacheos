//go:build release

package pkgsig

import "testing"

// 正式版本必须内置至少一把正式升级包签名公钥，且不含演示公钥（使用 etos-pkg 密钥仪式）。
func TestReleasePackageKeys(t *testing.T) {
	if len(demoKeys) != 0 {
		t.Fatal("release build contains demo package keys")
	}
	n := 0
	for kid := range builtinKeys {
		if !KIDRe.MatchString(kid) {
			t.Fatalf("bad production package kid %q", kid)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no production package signing key in internal/pkgsig/keys.go")
	}
}
