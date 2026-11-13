package conn

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

func upgradeSigJSON(t *testing.T, name, version, arch string) ([]byte, string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := pkgsig.Sign(priv, "etos-ci-pkg-test", name, version, arch, "standard", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	return b, s.SHA256
}

func TestCheckUpgradeRequest(t *testing.T) {
	sigJSON, digest := upgradeSigJSON(t, "sycache-agent", "1.2.3", runtime.GOARCH)
	if _, err := checkUpgradeRequest("sycache-agent", "1.2.3", digest, sigJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := checkUpgradeRequest("sycache-agent", "1.2.3", "", sigJSON); err != nil {
		t.Fatalf("absent sha256 must be allowed: %v", err)
	}
	if _, err := checkUpgradeRequest("sycache-agent", "1.2.3", "00"+digest[2:], sigJSON); err == nil {
		t.Error("sha256 different from signed metadata accepted")
	}
	if _, err := checkUpgradeRequest("sycache-agent", "1.2.4", digest, sigJSON); err == nil {
		t.Error("version mismatch accepted")
	}
	if _, err := checkUpgradeRequest("sycache", "1.2.3", digest, sigJSON); err == nil {
		t.Error("name mismatch accepted")
	}
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	badArch, d2 := upgradeSigJSON(t, "sycache-agent", "1.2.3", other)
	if _, err := checkUpgradeRequest("sycache-agent", "1.2.3", d2, badArch); err == nil {
		t.Error("foreign arch accepted")
	}
	if _, err := checkUpgradeRequest("sycache-agent", "1.2.3; rm", digest, sigJSON); err == nil {
		t.Error("bad version accepted")
	}
}
