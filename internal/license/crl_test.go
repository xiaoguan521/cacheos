package license

import (
	"strings"
	"testing"
	"time"
)

func crlFixture(t *testing.T) (KeyRing, *CRL, func(*CRL) string) {
	keys, priv, _, _ := fixture(t)
	c := &CRL{V: 1, KID: "etos-test", Serial: 3, IssuedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		RevokedLicenses: []RevokedLicense{{LicenseID: "LIC-TEST-1", RevokedAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), Reason: "refund"}}}
	sign := func(c *CRL) string {
		s, err := SignCRL(c, priv)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	return keys, c, sign
}

func TestCRLRoundTrip(t *testing.T) {
	keys, c, sign := crlFixture(t)
	got, err := VerifyCRL(sign(c), keys)
	if err != nil {
		t.Fatal(err)
	}
	if got.Serial != 3 || !got.LicenseRevoked("LIC-TEST-1") || got.LicenseRevoked("LIC-OTHER") {
		t.Fatalf("%+v", got)
	}
}

// 域分离：CRL 的签名不能当 License 用，License 的签名也不能当 CRL 用。
func TestCRLDomainSeparation(t *testing.T) {
	keys, priv, p, _ := fixture(t)
	lic := mustSign(t, p, priv)
	if _, err := VerifyCRL(lic, keys); err == nil {
		t.Fatal("a license verified as a CRL")
	}
	_, c, sign := crlFixture(t)
	_ = c
	raw := strings.TrimPrefix(sign(&CRL{V: 1, KID: "etos-test", Serial: 1}), crlPrefix)
	if _, err := Verify(raw, keys); err == nil {
		t.Fatal("a CRL verified as a license")
	}
}

func TestCRLRejects(t *testing.T) {
	keys, c, sign := crlFixture(t)
	raw := sign(c)
	body := strings.TrimPrefix(raw, crlPrefix)
	parts := strings.Split(body, ".")
	tampered := crlPrefix + parts[0][:len(parts[0])-2] + "AA." + parts[1]
	for name, bad := range map[string]string{
		"tampered":    tampered,
		"no prefix":   body,
		"garbage":     crlPrefix + "x.y",
		"unknown kid": func() string { c2 := *c; c2.KID = "nope"; return sign(&c2) }(),
	} {
		if _, err := VerifyCRL(bad, keys); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestEvaluateRejectsRevoked(t *testing.T) {
	keys, priv, p, env := fixture(t)
	raw := mustSign(t, p, priv)
	if s := Evaluate(raw, keys, env); s.State != StateValid {
		t.Fatalf("baseline %s", s.State)
	}
	env.CRL = &CRL{RevokedLicenses: []RevokedLicense{{LicenseID: p.LicenseID}}}
	if s := Evaluate(raw, keys, env); s.State != StateInvalid || !strings.Contains(s.Reason, "revoked") {
		t.Fatalf("revoked license: %s %s", s.State, s.Reason)
	}
	env.CRL = &CRL{RevokedKIDs: []string{p.KID}}
	if s := Evaluate(raw, keys, env); s.State != StateInvalid {
		t.Fatalf("revoked kid: %s %s", s.State, s.Reason)
	}
}

// 内置吊销的 kid 不会出现在公钥环里。
func TestBuiltinRevokedKIDsExcluded(t *testing.T) {
	old := revokedKIDs
	defer func() { revokedKIDs = old }()
	revokedKIDs = map[string]bool{}
	kr := BuiltinKeyRing()
	for kid := range kr {
		revokedKIDs[kid] = true
	}
	if len(BuiltinKeyRing()) != 0 {
		t.Fatal("revoked kid still in key ring")
	}
}

func TestNewerCRL(t *testing.T) {
	a, b := &CRL{Serial: 2}, &CRL{Serial: 5}
	if NewerCRL(a, b) != b || NewerCRL(b, a) != b || NewerCRL(nil, a) != a || NewerCRL(a, nil) != a {
		t.Fatal("NewerCRL must keep the highest serial")
	}
}
