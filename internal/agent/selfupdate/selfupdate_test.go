package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

func pkg(t *testing.T, bin string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: binEntry, Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(bin))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestInstallAndStaged(t *testing.T) {
	state := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]ed25519.PublicKey{"etos-ci-pkg-test": pub}
	data := pkg(t, "#!/bin/sh\necho new\n")
	sig, _ := pkgsig.Sign(priv, "etos-ci-pkg-test", "sycache-agent", "1.2.0", "amd64", "standard", data)
	sb, _ := json.Marshal(sig)

	// 数据面的包、错误架构、错误签名都拒绝
	ssig, _ := pkgsig.Sign(priv, "etos-ci-pkg-test", "sycache", "1.2.0", "amd64", "standard", data)
	ssb, _ := json.Marshal(ssig)
	if _, err := Install(state, data, ssb, "amd64", keys); err == nil {
		t.Fatal("data-plane package accepted as agent")
	}
	if _, err := Install(state, data, sb, "arm64", keys); err == nil {
		t.Fatal("wrong arch accepted")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	osig, _ := pkgsig.Sign(other, "etos-ci-pkg-test", "sycache-agent", "1.2.0", "amd64", "standard", data)
	osb, _ := json.Marshal(osig)
	if _, err := Install(state, data, osb, "amd64", keys); err == nil {
		t.Fatal("bad signature accepted")
	}

	ver, err := Install(state, data, sb, "amd64", keys)
	if err != nil || ver != "1.2.0" {
		t.Fatal(ver, err)
	}
	if bin := Staged(state, "1.1.9", "amd64", keys, nil); bin == "" {
		t.Fatal("newer staged version not found")
	}
	if bin := Staged(state, "1.2.0", "amd64", keys, nil); bin != "" {
		t.Fatal("same version must not be executed")
	}
	if bin := Staged(state, "1.10.0", "amd64", keys, nil); bin != "" {
		t.Fatal("older staged version must not be executed")
	}
	// 二进制被替换：不执行
	_ = os.WriteFile(filepath.Join(state, "agent/1.2.0/sycache-agent"), []byte("#!/bin/sh\necho evil\n"), 0o700)
	if bin := Staged(state, "1.1.9", "amd64", keys, nil); bin != "" {
		t.Fatal("tampered binary would be executed")
	}
}

// 防递归标记沿用本产品前缀，不再使用旧项目前缀。
func TestSelfUpdateGuardName(t *testing.T) {
	if envGuard != "SYCACHE_AGENT_SELFUPDATE_EXEC" {
		t.Fatalf("envGuard = %q", envGuard)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.2.0", "1.1.9", true}, {"1.10.0", "1.9.9", true}, {"1.2.0-2", "1.2.0-1", true}, {"1.2.0", "1.2.0", false}, {"1.2.0", "1.2.0-1", false}} {
		if newer(c.a, c.b) != c.want {
			t.Errorf("newer(%s,%s) != %v", c.a, c.b, c.want)
		}
	}
}

func TestInstallRejectsOversizePackage(t *testing.T) {
	big := make([]byte, pkgsig.MaxPackage+1)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]ed25519.PublicKey{"etos-ci-pkg-test": pub}
	sig, _ := pkgsig.Sign(priv, "etos-ci-pkg-test", "sycache-agent", "1.2.0", "amd64", "standard", big)
	sb, _ := json.Marshal(sig)
	if _, err := Install(t.TempDir(), big, sb, "amd64", keys); err == nil {
		t.Fatal("oversize agent package accepted")
	}
}

func TestStagedLogsTamperedInstall(t *testing.T) {
	state := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]ed25519.PublicKey{"etos-ci-pkg-test": pub}
	data := pkg(t, "#!/bin/sh\necho new\n")
	sig, _ := pkgsig.Sign(priv, "etos-ci-pkg-test", "sycache-agent", "1.2.0", "amd64", "standard", data)
	sb, _ := json.Marshal(sig)
	if _, err := Install(state, data, sb, "amd64", keys); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	// 没有新版本：正常情况，不记日志
	if Staged(state, "1.2.0", "amd64", keys, log) != "" || buf.Len() != 0 {
		t.Fatalf("up-to-date host must stay silent: %q", buf.String())
	}
	_ = os.WriteFile(filepath.Join(state, "agent/1.2.0/sycache-agent"), []byte("evil"), 0o700)
	if Staged(state, "1.1.9", "amd64", keys, log) != "" {
		t.Fatal("tampered binary would be executed")
	}
	if !strings.Contains(buf.String(), "1.2.0") || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("tampered install left no trace: %q", buf.String())
	}
}
