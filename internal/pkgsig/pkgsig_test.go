package pkgsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]ed25519.PublicKey{"etos-ci-pkg-test": pub}
	data := []byte("package bytes")
	s, err := Sign(priv, "etos-ci-pkg-test", "sycache", "1.30.6-1", "amd64", "standard", data)
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
		"kid":     func(x *Sig) { x.KID = "etos-pkg-2099" },
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
	if _, err := Sign(priv, "etos-ci-pkg-test", "sycache", "1.30.6;rm", "amd64", "standard", data); err == nil {
		t.Fatal("bad version accepted")
	}
	if _, err := Sign(priv, "etos-ci-pkg-test", "sycache", "1.0.0-dev", "amd64", "standard", data); err != nil {
		t.Fatalf("development package version rejected: %v", err)
	}
	if _, err := Sign(priv, "etos-prod-2027", "sycache", "1.30.6", "amd64", "standard", data); err == nil {
		t.Fatal("license key id accepted for packages")
	}
}

func TestSyEdgeWireFormat(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("syedge-compatible package bytes")
	sum := sha256.Sum256(data)
	s := &Sig{
		Format:  "etos-pkg-sig-1",
		KID:     "etos-pkg-2027",
		Name:    "sycache",
		Version: "1.30.6-1",
		Arch:    "amd64",
		Variant: "standard",
		SHA256:  hex.EncodeToString(sum[:]),
	}
	message := []byte("ETOS-PKG-v1\nsycache\n1.30.6-1\namd64\nstandard\n" + s.SHA256)
	s.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, message))
	if err := Verify(s, data, map[string]ed25519.PublicKey{"etos-pkg-2027": pub}); err != nil {
		t.Fatalf("SyEdge wire-format signature rejected: %v", err)
	}
}

func TestDemoKeyParses(t *testing.T) {
	for kid := range demoKeys {
		if _, ok := Keys()[kid]; !ok {
			t.Fatalf("demo key %s does not parse", kid)
		}
	}
}
