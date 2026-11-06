package engineupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

func releasePackage(t *testing.T, version string) ([]byte, []byte, map[string]ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	entries := []struct {
		name string
		mode int64
		body string
	}{
		{"./opt/sycache/bin/sycache-server", 0o755, "server"},
		{"./opt/sycache/bin/sycache-sentinel", 0o755, "server"},
		{"./opt/sycache/share/BUILDINFO", 0o644, "product_version=" + version + "\n"},
		{"./usr/lib/systemd/system/sycache@.service", 0o644, "ignored"},
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	sig, err := pkgsig.Sign(priv, "cacheos-ci-pkg-test", "sycache", version, "amd64", "standard", data)
	if err != nil {
		t.Fatal(err)
	}
	sigJSON, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	return data, sigJSON, map[string]ed25519.PublicKey{"cacheos-ci-pkg-test": pub}
}

func TestInstallVerifiesAndAtomicallyPublishesVersion(t *testing.T) {
	data, sigJSON, keys := releasePackage(t, "1.2.3")
	root := t.TempDir()
	if got, err := Install(root, data, sigJSON, "amd64", keys); err != nil || got != "1.2.3" {
		t.Fatalf("install: %s %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "versions/1.2.3/bin/sycache-server")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "versions/1.2.3/usr")); !os.IsNotExist(err) {
		t.Fatalf("system packaging files must not be copied: %v", err)
	}
	if _, err := Install(root, data, sigJSON, "amd64", keys); err == nil {
		t.Fatal("duplicate version accepted")
	}
}

func TestInstallRejectsTraversalAndBuildInfoMismatch(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := []byte("escape")
	if err := tw.WriteHeader(&tar.Header{Name: "./opt/sycache/../../escape", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = zw.Close()
	if err := extract(buf.Bytes(), t.TempDir()); err == nil {
		t.Fatal("path traversal package accepted")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin/sycache-server"), []byte("server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "share/BUILDINFO"), []byte("product_version=1.2.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateInstalledTree(root, "1.2.3"); err == nil {
		t.Fatal("BUILDINFO mismatch accepted")
	}
}
