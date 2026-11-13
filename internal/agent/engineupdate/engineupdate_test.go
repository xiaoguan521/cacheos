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
		{"./home/atwasoft/sycache/bin/sycache-server", 0o755, "server"},
		{"./home/atwasoft/sycache/bin/sycache-sentinel", 0o755, "server"},
		{"./home/atwasoft/sycache/share/BUILDINFO", 0o644, "product_version=" + version + "\n"},
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
	sig, err := pkgsig.Sign(priv, "etos-ci-pkg-test", "sycache", version, "amd64", "standard", data)
	if err != nil {
		t.Fatal(err)
	}
	sigJSON, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	return data, sigJSON, map[string]ed25519.PublicKey{"etos-ci-pkg-test": pub}
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
	if err := tw.WriteHeader(&tar.Header{Name: "./home/atwasoft/sycache/../../../escape", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
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

// 升级前要知道新包带了哪些模块：只认 modules/<名称>.so 的普通文件，其余一律忽略。
func TestPackageModules(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range []struct {
		name string
		typ  byte
	}{
		{"./home/atwasoft/sycache/modules/json.so", tar.TypeReg},
		{"./home/atwasoft/sycache/modules/bloom.so", tar.TypeReg},
		{"./home/atwasoft/sycache/modules/search.so", tar.TypeSymlink}, // 软链不算
		{"./home/atwasoft/sycache/modules/evil.so.txt", tar.TypeReg},
		{"./home/atwasoft/sycache/modules/sub/other.so", tar.TypeReg},
		{"./home/atwasoft/sycache/bin/sycache-server", tar.TypeReg},
		{"./usr/lib/modules/search.so", tar.TypeReg}, // 不在版本目录前缀内
	} {
		h := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: e.typ}
		if e.typ == tar.TypeSymlink {
			h.Linkname = "json.so"
		} else {
			h.Size = 1
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte("x"))
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	got, err := PackageModules(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["json"] || !got["bloom"] {
		t.Errorf("modules = %v, want json and bloom only", got)
	}
	if _, err := PackageModules([]byte("not gzip")); err == nil {
		t.Error("invalid archive accepted")
	}
}
