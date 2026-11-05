package selfupdate

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
	keys := map[string]ed25519.PublicKey{"cacheos-ci-pkg-test": pub}
	data := pkg(t, "#!/bin/sh\necho new\n")
	sig, _ := pkgsig.Sign(priv, "cacheos-ci-pkg-test", "sycache-agent", "1.2.0", "amd64", "standard", data)
	sb, _ := json.Marshal(sig)

	// 数据面的包、错误架构、错误签名都拒绝
	ssig, _ := pkgsig.Sign(priv, "cacheos-ci-pkg-test", "sycache", "1.2.0", "amd64", "standard", data)
	ssb, _ := json.Marshal(ssig)
	if _, err := Install(state, data, ssb, "amd64", keys); err == nil {
		t.Fatal("data-plane package accepted as agent")
	}
	if _, err := Install(state, data, sb, "arm64", keys); err == nil {
		t.Fatal("wrong arch accepted")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	osig, _ := pkgsig.Sign(other, "cacheos-ci-pkg-test", "sycache-agent", "1.2.0", "amd64", "standard", data)
	osb, _ := json.Marshal(osig)
	if _, err := Install(state, data, osb, "amd64", keys); err == nil {
		t.Fatal("bad signature accepted")
	}

	ver, err := Install(state, data, sb, "amd64", keys)
	if err != nil || ver != "1.2.0" {
		t.Fatal(ver, err)
	}
	if bin := Staged(state, "1.1.9", "amd64", keys); bin == "" {
		t.Fatal("newer staged version not found")
	}
	if bin := Staged(state, "1.2.0", "amd64", keys); bin != "" {
		t.Fatal("same version must not be executed")
	}
	if bin := Staged(state, "1.10.0", "amd64", keys); bin != "" {
		t.Fatal("older staged version must not be executed")
	}
	// 二进制被替换：不执行
	_ = os.WriteFile(filepath.Join(state, "agent/1.2.0/sycache-agent"), []byte("#!/bin/sh\necho evil\n"), 0o700)
	if bin := Staged(state, "1.1.9", "amd64", keys); bin != "" {
		t.Fatal("tampered binary would be executed")
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
