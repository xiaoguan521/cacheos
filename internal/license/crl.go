package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CRL 是厂商签名的吊销列表（规格书 8.7）：吊销的 License 编号与吊销的签名密钥编号。
//
// CRL 是累积的：新 CRL 包含之前所有吊销项，序号单调递增；控制台只保留序号最大的一份。
// 文件格式：crlPrefix + base64url(json) + "." + base64url(ed25519(crlContext + json))。
// 签名覆盖带上下文前缀的字节，因此 CRL 签名不能被当作 License 签名使用，反之亦然。
type CRL struct {
	V               int              `json:"v"`
	KID             string           `json:"kid"`
	Serial          uint64           `json:"serial"`
	IssuedAt        time.Time        `json:"issued_at"`
	RevokedLicenses []RevokedLicense `json:"revoked_licenses"`
	RevokedKIDs     []string         `json:"revoked_kids,omitempty"`
}

// RevokedLicense 是一条吊销记录。
type RevokedLicense struct {
	LicenseID string    `json:"license_id"`
	RevokedAt time.Time `json:"revoked_at"`
	Reason    string    `json:"reason,omitempty"`
}

const (
	crlPrefix  = "etos-crl1."
	crlContext = "ETOS-CRL-v1\n"
)

var ErrCRL = errors.New("license: invalid revocation list")

// LicenseRevoked 报告某个 License 是否在列表中。
func (c *CRL) LicenseRevoked(id string) bool {
	if c == nil {
		return false
	}
	for _, r := range c.RevokedLicenses {
		if r.LicenseID == id {
			return true
		}
	}
	return false
}

// KIDRevoked 报告某个签名密钥编号是否被吊销。
func (c *CRL) KIDRevoked(kid string) bool {
	if c == nil {
		return false
	}
	for _, k := range c.RevokedKIDs {
		if k == kid {
			return true
		}
	}
	return false
}

// CRLSigningInput 返回 CRL 签名所覆盖的字节（离线签名工具与测试使用）。
func CRLSigningInput(body []byte) []byte {
	return append([]byte(crlContext), body...)
}

// SignCRL 签名 CRL。仅供离线签名工具与测试使用。
func SignCRL(c *CRL, priv ed25519.PrivateKey) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return EncodeCRL(body, ed25519.Sign(priv, CRLSigningInput(body))), nil
}

// EncodeCRL 把 CRL 的 JSON 与签名编码为文件内容。
func EncodeCRL(body, sig []byte) string {
	return crlPrefix + base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// VerifyCRL 校验签名并解析 CRL。
func VerifyCRL(raw string, keys KeyRing) (*CRL, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, crlPrefix) {
		return nil, fmt.Errorf("%w: not a revocation list", ErrCRL)
	}
	parts := strings.Split(strings.TrimPrefix(raw, crlPrefix), ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: malformed", ErrCRL)
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: malformed body", ErrCRL)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: malformed signature", ErrCRL)
	}
	var head struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCRL, err)
	}
	pub, ok := keys[head.KID]
	if !ok {
		return nil, fmt.Errorf("%w: %v", ErrCRL, ErrUnknownKID)
	}
	if !ed25519.Verify(pub, CRLSigningInput(body), sig) {
		return nil, fmt.Errorf("%w: bad signature", ErrCRL)
	}
	var c CRL
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCRL, err)
	}
	if c.V != 1 {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCRL, c.V)
	}
	return &c, nil
}

// NewerCRL 返回两份 CRL 中序号较大的一份（CRL 是累积的，较新的包含较旧的全部吊销项）。
func NewerCRL(a, b *CRL) *CRL {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Serial > a.Serial:
		return b
	default:
		return a
	}
}
