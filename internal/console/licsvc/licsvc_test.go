package licsvc

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/license"
)

func setup(t *testing.T) (*Service, ed25519.PrivateKey) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := &Service{Store: storetest.Open(t), DataDir: t.TempDir(), Keys: license.KeyRing{"etos-prod-2027": pub}}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func lic(t *testing.T, s *Service, priv ed25519.PrivateKey, id string) string {
	now := time.Now().UTC().Truncate(time.Second)
	raw, err := license.Sign(&license.Payload{V: 1, KID: "etos-prod-2027", LicenseID: id, Customer: license.Customer{Name: "c"},
		Edition: license.EditionStandard, Features: []license.Feature{license.FeatureCore}, Limits: license.Limits{Hosts: 5},
		IssuedAt: now, NotBefore: now.Add(-time.Hour), ExpiresAt: now.AddDate(1, 0, 0), GraceDays: 15,
		Binding: license.Binding{Type: "installation_only", InstallationID: s.InstallationID()},
		Product: license.Product{Name: license.ProductName}}, priv)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func crl(t *testing.T, priv ed25519.PrivateKey, serial uint64, ids ...string) string {
	c := &license.CRL{V: 1, KID: "etos-prod-2027", Serial: serial, IssuedAt: time.Now().UTC()}
	for _, id := range ids {
		c.RevokedLicenses = append(c.RevokedLicenses, license.RevokedLicense{LicenseID: id, RevokedAt: time.Now().UTC()})
	}
	raw, err := license.SignCRL(c, priv)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRevokedLicenseBecomesInvalid(t *testing.T) {
	s, priv := setup(t)
	if snap, err := s.Import(lic(t, s, priv, "LIC-A"), "admin"); err != nil || snap.State != license.StateValid {
		t.Fatalf("import: %v %v", snap.State, err)
	}
	if _, err := s.ImportCRL(crl(t, priv, 1, "LIC-A")); err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.State != license.StateInvalid || !strings.Contains(snap.Reason, "revoked") {
		t.Fatalf("after CRL: %s %s", snap.State, snap.Reason)
	}
	if info := s.CRLInfo(); info == nil || info.Serial != 1 || info.Revoked != 1 {
		t.Fatalf("crl info %+v", info)
	}
	// 新签发的 License 可以替换被吊销的那张
	if snap, err := s.Import(lic(t, s, priv, "LIC-B"), "admin"); err != nil || snap.State != license.StateValid {
		t.Fatalf("replacement: %v %v", snap.State, err)
	}
}

// 不能用旧的吊销列表覆盖新的（否则可以“撤销吊销”）。
func TestCRLMustBeNewer(t *testing.T) {
	s, priv := setup(t)
	if _, err := s.ImportCRL(crl(t, priv, 5, "LIC-A")); err != nil {
		t.Fatal(err)
	}
	for _, serial := range []uint64{5, 4} {
		if _, err := s.ImportCRL(crl(t, priv, serial)); err == nil {
			t.Fatalf("serial %d accepted after 5", serial)
		}
	}
	if _, err := s.ImportCRL(crl(t, priv, 6, "LIC-A", "LIC-C")); err != nil {
		t.Fatal(err)
	}
	// 伪造的（另一把密钥签的）不接受
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := s.ImportCRL(crl(t, other, 99)); err == nil {
		t.Fatal("CRL signed by an unknown key accepted")
	}
}

// 授权文件可以同时带 License 与吊销列表；吊销列表先导入，所以已吊销的 License 导入不进来。
func TestBundleImport(t *testing.T) {
	s, priv := setup(t)
	bundle := "# customer bundle\n" + lic(t, s, priv, "LIC-A") + "\n" + crl(t, priv, 2, "LIC-OLD") + "\n"
	snap, err := s.Import(bundle, "admin")
	if err != nil || snap.State != license.StateValid || s.CRLInfo().Serial != 2 {
		t.Fatalf("bundle: %v %v %+v", snap.State, err, s.CRLInfo())
	}
	revokedBundle := lic(t, s, priv, "LIC-X") + "\n" + crl(t, priv, 3, "LIC-OLD", "LIC-X")
	if _, err := s.Import(revokedBundle, "admin"); err == nil {
		t.Fatal("revoked license imported")
	}
	if s.Snapshot().Payload.LicenseID != "LIC-A" {
		t.Fatal("current license replaced by a rejected one")
	}
	// 再次导入同一份吊销列表（序号相同）不算错误
	if _, err := s.Import(lic(t, s, priv, "LIC-D")+"\n"+crl(t, priv, 3, "LIC-OLD", "LIC-X"), "admin"); err != nil {
		t.Fatalf("same CRL again: %v", err)
	}
}
