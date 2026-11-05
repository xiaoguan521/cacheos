// Package sm2cert 解析国密 SM2 证书与私钥（P8 国密变体，规格书 12.6）。
//
// Go 标准库不支持 SM2 曲线（x509.ParseCertificate 会因“unsupported elliptic curve”整张拒绝），
// 这里只做控制台需要的部分：读出主题、域名、有效期、密钥用途，并校验私钥与证书公钥匹配。
// 不验证证书签名（SM2-with-SM3），也不做握手；证书链与握手由数据面（铜锁）在 syapp -t 和运行时负责。
//
// SM2 曲线 a = p - 3，可用标准库通用的 elliptic.CurveParams 计算 d·G 来比对公钥；
// 只在控制台上校验上传的私钥，不用于签名或密钥协商。
package sm2cert

import (
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// 证书用途：TLCP（GB/T 38636）双证书体系中，签名证书与加密证书分开。
const (
	UsageSign = "sign"
	UsageEnc  = "enc"
)

var (
	// OIDSM2 是 SM2 曲线（GB/T 33560）。
	OIDSM2         = asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 301}
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSAN         = asn1.ObjectIdentifier{2, 5, 29, 17}
	oidKeyUsage    = asn1.ObjectIdentifier{2, 5, 29, 15}
)

var (
	curveOnce sync.Once
	curve     *elliptic.CurveParams
)

func hexInt(s string) *big.Int { v, _ := new(big.Int).SetString(s, 16); return v }

// Curve 返回 SM2 推荐曲线参数（GB/T 32918.5）。
func Curve() elliptic.Curve {
	curveOnce.Do(func() {
		curve = &elliptic.CurveParams{
			Name:    "SM2",
			P:       hexInt("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF00000000FFFFFFFFFFFFFFFF"),
			N:       hexInt("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFF7203DF6B21C6052B53BBF40939D54123"),
			B:       hexInt("28E9FA9E9D9F5E344D5A9E4BCF6509A7F39789F515AB8F92DDBCBD414D940E93"),
			Gx:      hexInt("32C4AE2C1F1981195F9904466A39C9948FE30BBFF2660BE1715A4589334C74C7"),
			Gy:      hexInt("BC3736A2F4F6779C59BDCEE36B692153D0A9877CC62A474002DF32E52139F0A0"),
			BitSize: 256,
		}
	})
	return curve
}

// Cert 是解析出的 SM2 证书信息。
type Cert struct {
	Raw         []byte    `json:"-"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	Serial      string    `json:"serial"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DNSNames    []string  `json:"dns_names"`
	KeyUsage    x509.KeyUsage
	HasKeyUsage bool
	x, y        *big.Int
}

type certificate struct {
	TBS    asn1.RawValue
	SigAlg pkix.AlgorithmIdentifier
	Sig    asn1.BitString
}

type tbsCertificate struct {
	Version  int `asn1:"optional,explicit,default:0,tag:0"`
	Serial   *big.Int
	SigAlg   pkix.AlgorithmIdentifier
	Issuer   asn1.RawValue
	Validity struct{ NotBefore, NotAfter time.Time }
	Subject  asn1.RawValue
	SPKI     struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	IssuerUID  asn1.BitString   `asn1:"optional,tag:1"`
	SubjectUID asn1.BitString   `asn1:"optional,tag:2"`
	Extensions []pkix.Extension `asn1:"omitempty,optional,explicit,tag:3"`
}

func name(raw asn1.RawValue) (pkix.Name, error) {
	var rdn pkix.RDNSequence
	if rest, err := asn1.Unmarshal(raw.FullBytes, &rdn); err != nil || len(rest) > 0 {
		return pkix.Name{}, errors.New("bad name")
	}
	var n pkix.Name
	n.FillFromRDNSequence(&rdn)
	return n, nil
}

func display(n pkix.Name) string {
	if n.CommonName != "" {
		return n.CommonName
	}
	if len(n.Organization) > 0 {
		return n.Organization[0]
	}
	return ""
}

// ParseCertificate 解析一张 DER 编码的 SM2 证书；公钥不是 SM2 曲线时返回错误。
func ParseCertificate(der []byte) (*Cert, error) {
	var c certificate
	if rest, err := asn1.Unmarshal(der, &c); err != nil || len(rest) > 0 {
		return nil, errors.New("不是有效的 X.509 证书")
	}
	var t tbsCertificate
	if rest, err := asn1.Unmarshal(c.TBS.FullBytes, &t); err != nil || len(rest) > 0 {
		return nil, errors.New("不是有效的 X.509 证书")
	}
	alg := t.SPKI.Algorithm
	isSM2 := alg.Algorithm.Equal(OIDSM2)
	if alg.Algorithm.Equal(oidECPublicKey) {
		var p asn1.ObjectIdentifier
		if _, err := asn1.Unmarshal(alg.Parameters.FullBytes, &p); err == nil && p.Equal(OIDSM2) {
			isSM2 = true
		}
	}
	if !isSM2 {
		return nil, errors.New("证书公钥不是 SM2 算法")
	}
	pt := t.SPKI.PublicKey.RightAlign()
	if len(pt) != 65 || pt[0] != 4 {
		return nil, errors.New("证书公钥格式不支持（需要未压缩的 SM2 点）")
	}
	out := &Cert{Raw: der, x: new(big.Int).SetBytes(pt[1:33]), y: new(big.Int).SetBytes(pt[33:])}
	if !Curve().IsOnCurve(out.x, out.y) {
		return nil, errors.New("证书公钥不在 SM2 曲线上")
	}
	sub, err := name(t.Subject)
	if err != nil {
		return nil, errors.New("证书主题无法解析")
	}
	iss, err := name(t.Issuer)
	if err != nil {
		return nil, errors.New("证书颁发者无法解析")
	}
	out.Subject, out.Issuer = display(sub), display(iss)
	if t.Serial != nil {
		out.Serial = t.Serial.Text(16)
	}
	out.NotBefore, out.NotAfter = t.Validity.NotBefore.UTC(), t.Validity.NotAfter.UTC()
	for _, e := range t.Extensions {
		switch {
		case e.Id.Equal(oidSAN):
			var names []asn1.RawValue
			if _, err := asn1.Unmarshal(e.Value, &names); err != nil {
				return nil, errors.New("证书的备用名称扩展无法解析")
			}
			for _, n := range names {
				if n.Class == asn1.ClassContextSpecific && n.Tag == 2 {
					out.DNSNames = append(out.DNSNames, strings.ToLower(string(n.Bytes)))
				}
			}
		case e.Id.Equal(oidKeyUsage):
			var bs asn1.BitString
			if _, err := asn1.Unmarshal(e.Value, &bs); err != nil {
				return nil, errors.New("证书的密钥用途扩展无法解析")
			}
			for i := 0; i < 9; i++ {
				if bs.At(i) != 0 {
					out.KeyUsage |= 1 << uint(i)
				}
			}
			out.HasKeyUsage = true
		}
	}
	if len(out.DNSNames) == 0 && sub.CommonName != "" {
		out.DNSNames = []string{strings.ToLower(sub.CommonName)}
	}
	return out, nil
}

// VerifyHostname 检查证书是否覆盖主机名（精确匹配，或通配符只匹配一级）。
func (c *Cert) VerifyHostname(h string) error {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	for _, n := range c.DNSNames {
		if n == h {
			return nil
		}
		if strings.HasPrefix(n, "*.") {
			if i := strings.IndexByte(h, '.'); i > 0 && h[i:] == n[1:] {
				return nil
			}
		}
	}
	return fmt.Errorf("证书不包含域名 %s", h)
}

// Fingerprint 返回证书 DER 的 SHA-256。
func (c *Cert) Fingerprint() string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// MatchesKey 报告私钥 d 是否对应证书公钥。
func (c *Cert) MatchesKey(d *big.Int) bool {
	x, y := Curve().ScalarBaseMult(d.FillBytes(make([]byte, 32)))
	return x.Cmp(c.x) == 0 && y.Cmp(c.y) == 0
}

type ecPrivateKey struct {
	Version    int
	PrivateKey []byte
	NamedCurve asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
	PublicKey  asn1.BitString        `asn1:"optional,explicit,tag:1"`
}

type pkcs8 struct {
	Version    int
	Algo       pkix.AlgorithmIdentifier
	PrivateKey []byte
}

// ParsePrivateKey 解析 SM2 私钥（PKCS#8 或 SEC1），返回私钥标量。
func ParsePrivateKey(der []byte) (*big.Int, error) {
	var p pkcs8
	if rest, err := asn1.Unmarshal(der, &p); err == nil && len(rest) == 0 && len(p.PrivateKey) > 0 {
		ok := p.Algo.Algorithm.Equal(OIDSM2)
		if p.Algo.Algorithm.Equal(oidECPublicKey) {
			var oid asn1.ObjectIdentifier
			if _, err := asn1.Unmarshal(p.Algo.Parameters.FullBytes, &oid); err == nil && oid.Equal(OIDSM2) {
				ok = true
			}
		}
		if !ok {
			return nil, errors.New("私钥不是 SM2 算法")
		}
		der = p.PrivateKey
	}
	var k ecPrivateKey
	if rest, err := asn1.Unmarshal(der, &k); err != nil || len(rest) > 0 || k.Version != 1 {
		return nil, errors.New("私钥不是 PKCS#8 或 SEC1 格式")
	}
	if len(k.NamedCurve) > 0 && !k.NamedCurve.Equal(OIDSM2) {
		return nil, errors.New("私钥不是 SM2 曲线")
	}
	d := new(big.Int).SetBytes(k.PrivateKey)
	if d.Sign() <= 0 || d.Cmp(Curve().Params().N) >= 0 || len(k.PrivateKey) > 32 {
		return nil, errors.New("私钥数值无效")
	}
	return d, nil
}

// Parsed 是校验通过的一对证书与私钥。
type Parsed struct {
	*Cert
	ChainPEM string // 规范化后的证书链 PEM
	KeyPEM   string
}

// ParsePair 校验一对 SM2 证书（可带中间证书）与私钥：格式、算法、用途、私钥匹配、有效期。
// usage 为 UsageSign 或 UsageEnc，用于发现签名证书与加密证书放反。
func ParsePair(certPEM, keyPEM, usage string, now time.Time) (*Parsed, error) {
	certPEM, keyPEM = strings.TrimSpace(certPEM)+"\n", strings.TrimSpace(keyPEM)+"\n"
	what := map[string]string{UsageSign: "签名证书", UsageEnc: "加密证书"}[usage]
	if what == "" {
		return nil, fmt.Errorf("unknown usage %q", usage)
	}
	blk, rest := pem.Decode([]byte(certPEM))
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s：没有找到 PEM 格式的证书", what)
	}
	leaf, err := ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s：%v", what, err)
	}
	for {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%s：证书文件中出现了非证书内容（%s）", what, b.Type)
		}
	}
	if strings.Contains(keyPEM, "ENCRYPTED") {
		return nil, fmt.Errorf("%s的私钥带有密码保护，请先去掉密码再上传", what)
	}
	kb, _ := pem.Decode([]byte(keyPEM))
	if kb == nil || !strings.Contains(kb.Type, "PRIVATE KEY") {
		return nil, fmt.Errorf("%s：没有找到 PEM 格式的私钥", what)
	}
	d, err := ParsePrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s：%v", what, err)
	}
	if !leaf.MatchesKey(d) {
		return nil, fmt.Errorf("%s与私钥不匹配", what)
	}
	if !leaf.HasKeyUsage {
		return nil, fmt.Errorf("%s缺少密钥用途（Key Usage）扩展", what)
	}
	switch usage {
	case UsageSign:
		if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
			return nil, errors.New("这不是签名证书（密钥用途缺少 digitalSignature），签名证书与加密证书可能放反了")
		}
	case UsageEnc:
		if leaf.KeyUsage&(x509.KeyUsageKeyEncipherment|x509.KeyUsageDataEncipherment|x509.KeyUsageKeyAgreement) == 0 {
			return nil, errors.New("这不是加密证书（密钥用途缺少 keyEncipherment / dataEncipherment），签名证书与加密证书可能放反了")
		}
	}
	if !now.IsZero() && now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("%s已于 %s 过期", what, leaf.NotAfter.Format("2006-01-02"))
	}
	if len(leaf.DNSNames) == 0 {
		return nil, fmt.Errorf("%s中没有任何域名", what)
	}
	return &Parsed{Cert: leaf, ChainPEM: certPEM, KeyPEM: keyPEM}, nil
}
