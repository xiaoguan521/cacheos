package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

// builtinKeys 是正式签名密钥的公钥（按 kid）。私钥只存在于厂商的离线签名机（规格书 8.8.1），
// 绝不进入代码仓库、CI 或授权后台。新增或轮换密钥的步骤见 docs/license-ops.md。
//
// 演示密钥在 keys_demo.go 中，正式版本（-tags release）不包含它。
//
// 2026-11-06 密钥仪式：etos-prod-2027 为安装授权主密钥，-b 为备用密钥。
// 这里只内置公钥；对应私钥只保存在离线签名机上。
var builtinKeys = map[string]string{
	"etos-prod-2027":   "yOMQJB+/TEePp8sZxvA5D8D2C6RVI9/ki4yrY1RKOSc=",
	"etos-prod-2027-b": "67ObZdr/97DZ6Q250AYJ5u8MRXBzDqIwZ0HUy44jKDs=",
}

// revokedKIDs 是随控制台版本内置的已吊销签名密钥（私钥泄露或退役后加入，规格书 8.7）。
// 这里的 kid 即使出现在 builtinKeys 中也不会进入公钥环。
var revokedKIDs = map[string]bool{}

// ciKey 只在 CI 端到端测试的构建中通过 -ldflags 注入（每次运行临时生成，用完即弃），
// 正式与演示构建中为空。对应的 kid 固定为 CIKeyID。
var ciKey string

// CIKeyID 是 CI 临时密钥的编号。
const CIKeyID = "etos-ci-ephemeral"

// BuiltinKeyRing 返回内置公钥环。
func BuiltinKeyRing() KeyRing {
	kr := KeyRing{}
	if b, err := base64.StdEncoding.DecodeString(ciKey); err == nil && len(b) == ed25519.PublicKeySize {
		kr[CIKeyID] = ed25519.PublicKey(b)
	}
	for kid, b64 := range builtinKeys {
		if revokedKIDs[kid] {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && len(b) == ed25519.PublicKeySize {
			kr[kid] = ed25519.PublicKey(b)
		}
	}
	return kr
}

// AgentCheck 是 agent 侧的独立校验（规格书 8.6）：只验签名与有效期，不涉及控制台安装绑定。
// 返回 License 是否允许新的发布；任何结果都不会影响已有实例的运行。
func AgentCheck(raw string, keys KeyRing, now time.Time) (writable bool, reason string) {
	p, err := Verify(raw, keys)
	if err != nil {
		return false, err.Error()
	}
	if now.Before(p.NotBefore) {
		return false, reasonNotYetValid
	}
	if windowState(p, now) == StateExpired {
		return false, "license expired"
	}
	return true, ""
}

// DemoKeyID 是演示密钥的编号。
const DemoKeyID = "etos-demo-2026"

// ProductionKIDs 返回内置的正式签名密钥编号（不含演示与 CI 临时密钥、不含已吊销的）。
func ProductionKIDs() []string {
	var out []string
	for kid := range BuiltinKeyRing() {
		if kid != DemoKeyID && kid != CIKeyID {
			out = append(out, kid)
		}
	}
	return out
}

var prodKIDRe = regexp.MustCompile(`^etos-prod-[0-9]{4}(-[0-9a-z]{1,8})?$`)

// ParsePublicKey 校验正式密钥编号与 Base64 公钥（授权后台登记密钥时使用）。
func ParsePublicKey(kid, b64 string) (ed25519.PublicKey, error) {
	if !prodKIDRe.MatchString(kid) {
		return nil, errors.New("密钥编号必须形如 etos-prod-<年份>[-序号]")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("公钥必须是 Base64 编码的 32 字节 Ed25519 公钥")
	}
	return ed25519.PublicKey(b), nil
}

// KeyFingerprint 返回公钥指纹（SHA-256，每 4 位一组）。离线签名工具与授权后台显示同一格式，供人工核对。
func KeyFingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	s := strings.ToUpper(hex.EncodeToString(h[:]))
	var groups []string
	for i := 0; i < len(s); i += 4 {
		groups = append(groups, s[i:i+4])
	}
	return strings.Join(groups, " ")
}
