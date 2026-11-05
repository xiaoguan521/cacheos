// Package pki 实现控制台内置私有 CA：签发 gRPC 服务端证书与节点客户端证书（规格书 5.2、9.2）。
package pki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NodeCertValidity 是节点证书有效期。
const NodeCertValidity = 30 * 24 * time.Hour

// SpiffePrefix 是节点证书 URI SAN 的前缀。
const SpiffePrefix = "spiffe://cacheos/node/"

// legacySpiffePrefix keeps certificates issued by pre-CacheOS builds usable during an upgrade.
const legacySpiffePrefix = "spiffe://edgetraffic/node/"

// CA 是控制台的私有根证书。
type CA struct {
	Cert *x509.Certificate
	Key  ed25519.PrivateKey
	PEM  []byte
}

// LoadOrCreate 从 dir 读取 ca.crt / ca.key，不存在则生成（10 年）。
func LoadOrCreate(dir string) (*CA, error) {
	crtPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if crt, err := os.ReadFile(crtPath); err == nil {
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		return parse(crt, keyPEM)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "CacheOS Internal CA", Organization: []string{"CacheOS"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		return nil, err
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kder, _ := x509.MarshalPKCS8PrivateKey(priv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(crtPath, crt, 0o644); err != nil {
		return nil, err
	}
	return parse(crt, keyPEM)
}

func parse(crtPEM, keyPEM []byte) (*CA, error) {
	b, _ := pem.Decode(crtPEM)
	if b == nil {
		return nil, errors.New("pki: bad ca.crt")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errors.New("pki: bad ca.key")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("pki: ca.key is not ed25519")
	}
	return &CA{Cert: cert, Key: key, PEM: crtPEM}, nil
}

// Fingerprint 返回证书 DER 的 sha256（十六进制），用于 agent 首次连接时固定 CA。
func Fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

// ServerCert 为 gRPC 网关签发服务端证书（1 年），hosts 可含域名与 IP。
func (ca *CA) ServerCert(hosts []string) (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "sycache-console"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
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
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, pub, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: priv}, nil
}

// SignNode 校验 CSR 并为节点签发客户端证书。
func (ca *CA) SignNode(csrDER []byte, nodeID string) ([]byte, *big.Int, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, err
	}
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok {
		return nil, nil, errors.New("pki: node key must be ed25519")
	}
	u, _ := url.Parse(SpiffePrefix + nodeID)
	sn := serial()
	tpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "node-" + nodeID},
		URIs:         []*url.URL{u},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(NodeCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, csr.PublicKey, crypto.Signer(ca.Key))
	return der, sn, err
}

// NodeID 从已验证的客户端证书中取出节点 ID。
func NodeID(cert *x509.Certificate) (string, error) {
	for _, u := range cert.URIs {
		if s := u.String(); strings.HasPrefix(s, SpiffePrefix) {
			return strings.TrimPrefix(s, SpiffePrefix), nil
		}
		if s := u.String(); strings.HasPrefix(s, legacySpiffePrefix) {
			return strings.TrimPrefix(s, legacySpiffePrefix), nil
		}
	}
	return "", fmt.Errorf("pki: certificate has no node identity")
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}
