package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WebCA 为 Web 控制台签发 HTTPS 证书（-tls-auto）。
//
// 与节点 mTLS 使用的 CA 分开：节点 CA 是 Ed25519，而浏览器不接受 Ed25519 证书。
// WebCA 使用 ECDSA P-256，管理员把 web-ca.crt 导入浏览器一次后，自动换发的证书都受信任。
type WebCA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
	now  func() time.Time

	mu   sync.Mutex
	leaf *tls.Certificate
}

const (
	webCACert = "web-ca.crt"
	webCAKey  = "web-ca.key"
	// 浏览器对公开证书的上限是 398 天；内部 CA 同样遵守，避免被拒。
	webLeafValidity = 397 * 24 * time.Hour
	webRenewBefore  = 30 * 24 * time.Hour
)

// LoadOrCreateWebCA 读取或生成 Web CA（有效期 10 年），保存在 dir。
func LoadOrCreateWebCA(dir string) (*WebCA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cp, kp := filepath.Join(dir, webCACert), filepath.Join(dir, webCAKey)
	if cb, err := os.ReadFile(cp); err == nil {
		kb, err := os.ReadFile(kp)
		if err != nil {
			return nil, err
		}
		cblk, _ := pem.Decode(cb)
		kblk, _ := pem.Decode(kb)
		if cblk == nil || kblk == nil {
			return nil, errors.New("web CA files are corrupt")
		}
		cert, err := x509.ParseCertificate(cblk.Bytes)
		if err != nil {
			return nil, err
		}
		key, err := x509.ParseECPrivateKey(kblk.Bytes)
		if err != nil {
			return nil, err
		}
		return &WebCA{Cert: cert, key: key, now: time.Now}, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "CacheOS Console Web CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return &WebCA{Cert: cert, key: key, now: time.Now}, nil
}

// CertPEM 返回 CA 证书（供管理员下载导入浏览器）。
func (w *WebCA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: w.Cert.Raw})
}

// Leaf 签发一张覆盖 hosts 的服务端证书。
func (w *WebCA) Leaf(hosts []string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := w.now()
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "sycache-console"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(webLeafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, w.Cert, &key.PublicKey, w.key)
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der, w.Cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// GetCertificate 返回 tls.Config.GetCertificate：按需签发，剩余不足 30 天时自动换发。
func (w *WebCA) GetCertificate(hosts []string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.leaf == nil || w.now().After(w.leaf.Leaf.NotAfter.Add(-webRenewBefore)) {
			c, err := w.Leaf(hosts)
			if err != nil {
				return nil, err
			}
			w.leaf = c
		}
		return w.leaf, nil
	}
}
