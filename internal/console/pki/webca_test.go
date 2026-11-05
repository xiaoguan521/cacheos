package pki

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 浏览器不接受 Ed25519 证书，Web 控制台的自动 TLS 必须是 ECDSA，且有效期不超过 398 天。
func TestWebCAIssuesBrowserCompatibleCerts(t *testing.T) {
	dir := t.TempDir()
	w, err := LoadOrCreateWebCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Cert.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Fatalf("web CA key is %T, want ECDSA", w.Cert.PublicKey)
	}
	again, err := LoadOrCreateWebCA(dir)
	if err != nil || !again.Cert.Equal(w.Cert) {
		t.Fatal("web CA not persisted")
	}
	crt, err := w.Leaf([]string{"console.example.com", "10.0.0.5", "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(crt.Certificate[0])
	if _, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Fatal("leaf key is not ECDSA")
	}
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 398*24*time.Hour {
		t.Fatalf("leaf validity %v exceeds 398 days", d)
	}
	pool := x509.NewCertPool()
	pool.AddCert(w.Cert)
	for _, h := range []string{"console.example.com", "10.0.0.5", "localhost"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: h}); err != nil {
			t.Fatalf("verify %s: %v", h, err)
		}
	}

	// 真实 TLS 握手：GetCertificate 返回的证书可被只信任该 CA 的客户端接受
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{GetCertificate: w.GetCertificate([]string{"localhost", "127.0.0.1"})}
	srv.StartTLS()
	defer srv.Close()
	cli := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	res, err := cli.Get("https://localhost:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}

// 自动续期：剩余不足 30 天时 GetCertificate 换发新证书。
func TestWebLeafRenewsBeforeExpiry(t *testing.T) {
	w, err := LoadOrCreateWebCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	get := w.GetCertificate([]string{"localhost"})
	c1, err := get(nil)
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return time.Now().Add(380 * 24 * time.Hour) }
	c2, err := get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(c1.Certificate[0]) == string(c2.Certificate[0]) {
		t.Fatal("certificate not renewed near expiry")
	}
}
