// Package pkgsig 是数据面 / agent 升级包的厂商签名（规格书 12.6）。
//
// 升级包签名密钥与 License 签名密钥分开（kid 前缀 cacheos-pkg-），同样只在离线签名机上以口令加密文件存在。
// 签名对象带域分隔前缀，与 License、吊销列表的签名互不可替换：
//
//	"CACHEOS-PKG-v1\n" + name + "\n" + version + "\n" + arch + "\n" + variant + "\n" + sha256(包)
//
// 校验方：控制台导入版本库时、wrapper 以 root 使用升级包之前。
package pkgsig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Format 是签名文件格式标识。
const Format = "cacheos-pkg-sig-1"

const domain = "CACHEOS-PKG-v1\n"

var (
	VersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$`)
	KIDRe     = regexp.MustCompile(`^cacheos-pkg-[0-9]{4}(-[0-9a-z]{1,8})?$`)
	kidAnyRe  = regexp.MustCompile(`^cacheos-(pkg|demo-pkg|ci-pkg)-[0-9a-z-]{1,20}$`)

	ErrSignature = errors.New("package signature invalid")
)

// 允许的包名、架构与变体。
var (
	Names    = map[string]bool{"sycache": true, "sycache-agent": true}
	Arches   = map[string]bool{"amd64": true, "arm64": true}
	Variants = map[string]bool{"standard": true, "gm": true} // gm：铜锁国密变体（P6）
)

// Sig 是升级包旁边的签名文件（<包>.sig，JSON）。
type Sig struct {
	Format    string `json:"format"`
	KID       string `json:"kid"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	Variant   string `json:"variant"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

func (s *Sig) message() []byte {
	return []byte(domain + s.Name + "\n" + s.Version + "\n" + s.Arch + "\n" + s.Variant + "\n" + s.SHA256)
}

func (s *Sig) validFields() error {
	switch {
	case s.Format != Format:
		return fmt.Errorf("%w: unknown format %q", ErrSignature, s.Format)
	case !Names[s.Name]:
		return fmt.Errorf("%w: unknown package name %q", ErrSignature, s.Name)
	case !VersionRe.MatchString(s.Version):
		return fmt.Errorf("%w: bad version %q", ErrSignature, s.Version)
	case !Arches[s.Arch]:
		return fmt.Errorf("%w: bad arch %q", ErrSignature, s.Arch)
	case !Variants[s.Variant]:
		return fmt.Errorf("%w: bad variant %q", ErrSignature, s.Variant)
	case !kidAnyRe.MatchString(s.KID):
		return fmt.Errorf("%w: bad kid %q", ErrSignature, s.KID)
	}
	return nil
}

// Sign 为包内容生成签名文件。
func Sign(priv ed25519.PrivateKey, kid, name, version, arch, variant string, data []byte) (*Sig, error) {
	sum := sha256.Sum256(data)
	s := &Sig{Format: Format, KID: kid, Name: name, Version: version, Arch: arch, Variant: variant, SHA256: hex.EncodeToString(sum[:])}
	if err := s.validFields(); err != nil {
		return nil, err
	}
	s.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, s.message()))
	return s, nil
}

// Parse 解析签名文件。
func Parse(b []byte) (*Sig, error) {
	var s Sig
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignature, err)
	}
	return &s, s.validFields()
}

// Verify 用给定公钥集合校验签名与包内容（kid 不在集合内即失败）。
func Verify(s *Sig, data []byte, keys map[string]ed25519.PublicKey) error {
	if err := s.validFields(); err != nil {
		return err
	}
	pub, ok := keys[s.KID]
	if !ok || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: unknown signing key %q", ErrSignature, s.KID)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != s.SHA256 {
		return fmt.Errorf("%w: sha256 mismatch", ErrSignature)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil || !ed25519.Verify(pub, s.message(), sig) {
		return fmt.Errorf("%w: bad signature", ErrSignature)
	}
	return nil
}

// ciKey 只在 CI 端到端测试构建中通过 -ldflags 注入（临时生成），正式与演示构建中为空；kid 固定为 CIKeyID。
var ciKey string

// CIKeyID 是 CI 临时升级包签名密钥的编号。
const CIKeyID = "cacheos-ci-pkg-ephemeral"

// Keys 返回内置的升级包签名公钥（正式公钥在 keys.go，演示公钥只在非 release 构建中）。
func Keys() map[string]ed25519.PublicKey {
	out := map[string]ed25519.PublicKey{}
	for kid, b64 := range builtinKeys {
		if b, err := base64.StdEncoding.DecodeString(b64); err == nil && len(b) == ed25519.PublicKeySize {
			out[kid] = ed25519.PublicKey(b)
		}
	}
	if b, err := base64.StdEncoding.DecodeString(ciKey); err == nil && len(b) == ed25519.PublicKeySize {
		out[CIKeyID] = ed25519.PublicKey(b)
	}
	for kid, b64 := range demoKeys {
		if b, err := base64.StdEncoding.DecodeString(b64); err == nil && len(b) == ed25519.PublicKeySize {
			out[kid] = ed25519.PublicKey(b)
		}
	}
	return out
}
