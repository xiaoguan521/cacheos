package sm2cert_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/xiaoguan521/cacheos/internal/pkg/sm2cert"
	"github.com/xiaoguan521/cacheos/internal/pkg/sm2cert/sm2certtest"
)

func TestCurveParams(t *testing.T) {
	c := Curve()
	if !c.IsOnCurve(c.Params().Gx, c.Params().Gy) {
		t.Fatal("base point not on curve")
	}
	if x, y := c.ScalarBaseMult(c.Params().N.Bytes()); x.Sign() != 0 || y.Sign() != 0 {
		t.Fatal("n*G is not the point at infinity")
	}
}

func TestParsePairs(t *testing.T) {
	sign := sm2certtest.New(t, "www.example.com", "sign", []string{"www.example.com", "*.api.example.com"})
	enc := sm2certtest.New(t, "www.example.com", "enc", []string{"www.example.com"})

	c, err := ParsePair(sign.CertPEM, sign.KeyPEM, UsageSign, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "www.example.com" || len(c.DNSNames) != 2 {
		t.Fatalf("parsed %+v", c)
	}
	if c.VerifyHostname("a.api.example.com") != nil || c.VerifyHostname("a.b.api.example.com") == nil || c.VerifyHostname("other.com") == nil {
		t.Fatal("hostname matching")
	}
	if _, err := ParsePair(enc.CertPEM, enc.KeyPEM, UsageEnc, time.Now()); err != nil {
		t.Fatal(err)
	}
	// SEC1 格式的私钥同样接受
	if _, err := ParsePair(sign.CertPEM, sign.SEC1PEM, UsageSign, time.Now()); err != nil {
		t.Fatal(err)
	}

	bad := []struct {
		name, cert, key, usage, want string
	}{
		{"swapped sign/enc", enc.CertPEM, enc.KeyPEM, UsageSign, "签名证书"},
		{"swapped enc/sign", sign.CertPEM, sign.KeyPEM, UsageEnc, "加密证书"},
		{"key mismatch", sign.CertPEM, enc.KeyPEM, UsageSign, "不匹配"},
		{"not sm2", rsaCertPEM(t), sign.KeyPEM, UsageSign, "SM2"},
		{"no key", sign.CertPEM, "", UsageSign, "私钥"},
		{"encrypted key", sign.CertPEM, "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAA==\n-----END ENCRYPTED PRIVATE KEY-----\n", UsageSign, "密码"},
	}
	for _, b := range bad {
		if _, err := ParsePair(b.cert, b.key, b.usage, time.Now()); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%s: err = %v, want %q", b.name, err, b.want)
		}
	}
	if _, err := ParsePair(sign.CertPEM, sign.KeyPEM, UsageSign, time.Now().Add(400*24*time.Hour)); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Errorf("expired: %v", err)
	}
}

func rsaCertPEM(t *testing.T) string {
	t.Helper()
	// 一张 P-256 证书：不是 SM2，必须拒绝
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestRejectsGarbage(t *testing.T) {
	if _, err := ParseCertificate([]byte{0x30, 0x03, 0x02, 0x01, 0x01}); err == nil {
		t.Fatal("accepted garbage")
	}
	if _, err := ParsePrivateKey([]byte("nope")); err == nil {
		t.Fatal("accepted garbage key")
	}
	// 私钥标量越界
	k, _ := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
		NamedCurve asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
	}{1, Curve().Params().N.Bytes(), OIDSM2})
	if _, err := ParsePrivateKey(k); err == nil {
		t.Fatal("accepted d >= n")
	}
}

// 用系统 OpenSSL（3.x 内置 SM2）生成真实的 SM2-with-SM3 证书交叉验证；不支持时跳过。
func TestOpenSSLGenerated(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
			t.Skipf("openssl without SM2 support: %v %s", err, out)
		}
	}
	for _, u := range []struct{ name, ku string }{{"sign", "digitalSignature"}, {"enc", "keyEncipherment,dataEncipherment"}} {
		k, c := filepath.Join(dir, u.name+".key"), filepath.Join(dir, u.name+".crt")
		run("genpkey", "-algorithm", "SM2", "-out", k)
		run("req", "-new", "-x509", "-key", k, "-sm3", "-days", "30", "-subj", "/CN=gm.example.com",
			"-addext", "subjectAltName=DNS:gm.example.com", "-addext", "keyUsage=critical,"+u.ku, "-out", c)
		cb, _ := os.ReadFile(c)
		kb, _ := os.ReadFile(k)
		got, err := ParsePair(string(cb), string(kb), u.name, time.Now())
		if err != nil {
			t.Fatalf("%s: %v", u.name, err)
		}
		if got.VerifyHostname("gm.example.com") != nil || got.Issuer != "gm.example.com" {
			t.Fatalf("%s: parsed %+v", u.name, got)
		}
		sec1 := filepath.Join(dir, u.name+".sec1")
		run("pkey", "-in", k, "-outform", "DER", "-out", sec1)
		raw, _ := os.ReadFile(sec1)
		if _, err := ParsePair(string(cb), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})), u.name, time.Now()); err != nil {
			t.Fatalf("%s sec1: %v", u.name, err)
		}
	}
}
