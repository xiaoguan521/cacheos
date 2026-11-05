// Package license 实现商业授权的解析、验签与状态判断（规格书第 8 节）。
//
// 文件格式：base64url(payload_json) + "." + base64url(ed25519_signature)。
// 签名对象是 payload 的原始字节，不重新序列化。
//
// 业务代码只应通过 Snapshot 读取授权，不得自行解析 .lic（规格书 8.9）。
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

// Payload 是 License 的明文内容（规格书 8.3）。
type Payload struct {
	V                int       `json:"v"`
	KID              string    `json:"kid"`
	LicenseID        string    `json:"license_id"`
	Customer         Customer  `json:"customer"`
	Edition          Edition   `json:"edition"`
	Features         []Feature `json:"features"`
	Limits           Limits    `json:"limits"`
	IssuedAt         time.Time `json:"issued_at"`
	NotBefore        time.Time `json:"not_before"`
	ExpiresAt        time.Time `json:"expires_at"`
	MaintenanceUntil time.Time `json:"maintenance_until"`
	GraceDays        int       `json:"grace_days"`
	Binding          Binding   `json:"binding"`
	Product          Product   `json:"product"`
}

type Customer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Limits 中 0 表示不限。
type Limits struct {
	Hosts     int `json:"hosts"`
	Instances int `json:"instances"`
	MemoryGB  int `json:"memory_gb"` // 全部数据实例 maxmemory 之和
	AuditDays int `json:"audit_days"`
}

type Binding struct {
	Type           string   `json:"type"` // "installation" | "installation_only"
	InstallationID string   `json:"installation_id"`
	FPHashes       []string `json:"fp_hashes,omitempty"`
}

type Product struct {
	Name       string `json:"name"`
	MinVersion string `json:"min_version"`
	MaxVersion string `json:"max_version"`
}

// HasFeature 报告 License 是否包含某功能。
func (p *Payload) HasFeature(f Feature) bool {
	for _, x := range p.Features {
		if x == f {
			return true
		}
	}
	return false
}

var (
	ErrMalformed  = errors.New("license: malformed")
	ErrUnknownKID = errors.New("license: unknown or revoked signing key")
	ErrSignature  = errors.New("license: bad signature")
)

// KeyRing 保存控制台内置的厂商公钥，按 kid 索引。
// 已吊销的 kid 不应出现在 KeyRing 中。
type KeyRing map[string]ed25519.PublicKey

// Verify 校验签名并解析 payload。它不判断时间与绑定，那由 Evaluate 负责。
func Verify(raw string, keys KeyRing) (*Payload, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, ErrMalformed
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: payload: %v", ErrMalformed, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: signature encoding", ErrMalformed)
	}
	// 先只取 kid，选出公钥后再验签，最后才完整解析。
	var head struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	pub, ok := keys[head.KID]
	if !ok {
		return nil, ErrUnknownKID
	}
	if !ed25519.Verify(pub, body, sig) {
		return nil, ErrSignature
	}
	var p Payload
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if p.V != 1 {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrMalformed, p.V)
	}
	return &p, nil
}

// Sign 生成 License 字符串。仅供厂商授权后台与测试使用，控制台不持有私钥。
func Sign(p *Payload, priv ed25519.PrivateKey) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
