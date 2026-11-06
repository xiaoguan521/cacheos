package pkgsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]ed25519.PublicKey{"cacheos-ci-pkg-test": pub}
	data := []byte("package bytes")
	s, err := Sign(priv, "cacheos-ci-pkg-test", "sycache", "1.30.6-1", "amd64", "standard", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(s, data, keys); err != nil {
		t.Fatal(err)
	}
	// 内容、版本、架构、变体、包名任何一项被改动都失败
	for name, mut := range map[string]func(x *Sig){
		"version": func(x *Sig) { x.Version = "1.30.7" },
		"arch":    func(x *Sig) { x.Arch = "arm64" },
		"variant": func(x *Sig) { x.Variant = "gmssl" },
		"name":    func(x *Sig) { x.Name = "sycache-agent" },
		"kid":     func(x *Sig) { x.KID = "cacheos-pkg-2099" },
	} {
		c := *s
		mut(&c)
		if err := Verify(&c, data, keys); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: tampering not detected", name)
		}
	}
	if err := Verify(s, []byte("other"), keys); !errors.Is(err, ErrSignature) {
		t.Fatal("content tampering not detected")
	}
	// 非法字段在签名时就被拒绝
	if _, err := Sign(priv, "cacheos-ci-pkg-test", "sycache", "1.30.6;rm", "amd64", "standard", data); err == nil {
		t.Fatal("bad version accepted")
	}
	if _, err := Sign(priv, "cacheos-ci-pkg-test", "sycache", "1.0.0-dev", "amd64", "standard", data); err != nil {
		t.Fatalf("development package version rejected: %v", err)
	}
	if _, err := Sign(priv, "cacheos-prod-2027", "sycache", "1.30.6", "amd64", "standard", data); err == nil {
		t.Fatal("license key id accepted for packages")
	}
}

func TestDemoKeyParses(t *testing.T) {
	for kid := range demoKeys {
		if _, ok := Keys()[kid]; !ok {
			t.Fatalf("demo key %s does not parse", kid)
		}
	}
}
