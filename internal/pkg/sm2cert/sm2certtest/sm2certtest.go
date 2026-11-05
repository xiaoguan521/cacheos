// Package sm2certtest 为测试生成 SM2 证书与私钥（签名字段是随机字节，只用于解析与匹配测试，不能用于握手）。
package sm2certtest

import (
	"crypto/rand"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/xiaoguan521/cacheos/internal/pkg/sm2cert"
)

// Pair 是一张测试证书与私钥（PKCS#8 与 SEC1 两种 PEM）。
type Pair struct {
	CertPEM, KeyPEM, SEC1PEM string
}

var (
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSM2WithSM3  = asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 501}
	oidSAN         = asn1.ObjectIdentifier{2, 5, 29, 17}
	oidKeyUsage    = asn1.ObjectIdentifier{2, 5, 29, 15}
)

type tbs struct {
	Version   int `asn1:"explicit,tag:0"`
	Serial    *big.Int
	SigAlg    pkix.AlgorithmIdentifier
	Issuer    pkix.RDNSequence
	Validity  struct{ NotBefore, NotAfter time.Time }
	Subject   pkix.RDNSequence
	SPKI      spki
	Extension []pkix.Extension `asn1:"explicit,tag:3"`
}

type spki struct {
	Algorithm pkix.AlgorithmIdentifier
	PublicKey asn1.BitString
}

type cert struct {
	TBS    tbs
	SigAlg pkix.AlgorithmIdentifier
	Sig    asn1.BitString
}

type sec1 struct {
	Version    int
	PrivateKey []byte
	NamedCurve asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
}

type pkcs8 struct {
	Version    int
	Algo       pkix.AlgorithmIdentifier
	PrivateKey []byte
}

// New 生成一张自签名形式的 SM2 证书。usage 为 sign（digitalSignature）或 enc（keyEncipherment|dataEncipherment|keyAgreement）。
func New(t testing.TB, cn, usage string, dns []string) Pair {
	t.Helper()
	return NewValid(t, cn, usage, dns, time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour))
}

// NewValid 同 New，可指定有效期。
func NewValid(t testing.TB, cn, usage string, dns []string, notBefore, notAfter time.Time) Pair {
	t.Helper()
	c := sm2cert.Curve()
	n := c.Params().N
	d, err := rand.Int(rand.Reader, new(big.Int).Sub(n, big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	d.Add(d, big.NewInt(1))
	x, y := c.ScalarBaseMult(d.FillBytes(make([]byte, 32)))
	point := append([]byte{4}, append(x.FillBytes(make([]byte, 32)), y.FillBytes(make([]byte, 32))...)...)

	curve, _ := asn1.Marshal(sm2cert.OIDSM2)
	var names []asn1.RawValue
	for _, h := range dns {
		names = append(names, asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte(h)})
	}
	san, _ := asn1.Marshal(names)
	ku := asn1.BitString{Bytes: []byte{0x80}, BitLength: 1} // digitalSignature
	if usage == "enc" {
		ku = asn1.BitString{Bytes: []byte{0x38}, BitLength: 5} // keyEncipherment | dataEncipherment | keyAgreement
	}
	kub, _ := asn1.Marshal(ku)
	name := pkix.Name{CommonName: cn}.ToRDNSequence()
	sigAlg := pkix.AlgorithmIdentifier{Algorithm: oidSM2WithSM3}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tb := tbs{Version: 2, Serial: serial, SigAlg: sigAlg, Issuer: name, Subject: name,
		SPKI: spki{Algorithm: pkix.AlgorithmIdentifier{Algorithm: oidECPublicKey, Parameters: asn1.RawValue{FullBytes: curve}},
			PublicKey: asn1.BitString{Bytes: point, BitLength: len(point) * 8}},
		Extension: []pkix.Extension{{Id: oidKeyUsage, Critical: true, Value: kub}, {Id: oidSAN, Value: san}}}
	tb.Validity.NotBefore, tb.Validity.NotAfter = notBefore.UTC().Truncate(time.Second), notAfter.UTC().Truncate(time.Second)
	sig := make([]byte, 72)
	_, _ = rand.Read(sig)
	der, err := asn1.Marshal(cert{TBS: tb, SigAlg: sigAlg, Sig: asn1.BitString{Bytes: sig, BitLength: len(sig) * 8}})
	if err != nil {
		t.Fatal(err)
	}
	sk, _ := asn1.Marshal(sec1{Version: 1, PrivateKey: d.FillBytes(make([]byte, 32)), NamedCurve: sm2cert.OIDSM2})
	inner, _ := asn1.Marshal(sec1{Version: 1, PrivateKey: d.FillBytes(make([]byte, 32))})
	p8, _ := asn1.Marshal(pkcs8{Algo: pkix.AlgorithmIdentifier{Algorithm: oidECPublicKey, Parameters: asn1.RawValue{FullBytes: curve}}, PrivateKey: inner})
	return Pair{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})),
		SEC1PEM: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sk})),
	}
}
