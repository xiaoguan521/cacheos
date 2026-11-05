package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// 正式公钥表的格式检查：kid 命名、Base64 长度；演示 / CI 编号不能出现在正式表中。
func TestProductionKeyTable(t *testing.T) {
	for kid, b64 := range builtinKeys {
		if kid == DemoKeyID {
			continue // 演示构建由 keys_demo.go 加入
		}
		if kid == CIKeyID || strings.HasPrefix(kid, "etos-demo") || strings.HasPrefix(kid, "etos-ci") {
			t.Errorf("%s: demo / CI key ids must not be in the production table", kid)
		}
		if !strings.HasPrefix(kid, "etos-prod-") {
			t.Errorf("%s: production key ids must look like etos-prod-<year>[-n]", kid)
		}
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(b) != ed25519.PublicKeySize {
			t.Errorf("%s: not a base64 ed25519 public key", kid)
		}
	}
}

func TestParsePublicKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	b64 := base64.StdEncoding.EncodeToString(pub)
	if _, err := ParsePublicKey("etos-prod-2027", b64); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"etos-demo-2026", b64}, {"etos-prod-27", b64}, {"etos-prod-2027", "AAAA"}, {"etos-prod-2027", "not base64!"}} {
		if _, err := ParsePublicKey(c[0], c[1]); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
	if fp := KeyFingerprint(pub); len(strings.Split(fp, " ")) != 16 {
		t.Fatalf("fingerprint %q", fp)
	}
}
