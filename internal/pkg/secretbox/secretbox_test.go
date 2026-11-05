package secretbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenAndKeyFile(t *testing.T) {
	dir := t.TempDir()
	b, err := LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v err %v", fi.Mode().Perm(), err)
	}
	plain := "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n"
	sealed, err := b.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "PRIVATE KEY") {
		t.Fatalf("sealed value leaks plaintext or lacks version: %q", sealed)
	}
	s2, _ := b.Seal(plain)
	if s2 == sealed {
		t.Fatal("nonce reuse: two seals are identical")
	}
	// 同一密钥文件重新加载后可解密
	b2, err := LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := b2.Open(sealed)
	if err != nil || got != plain {
		t.Fatalf("open: %q %v", got, err)
	}
	// 篡改密文被拒绝
	bad := sealed[:len(sealed)-2] + "AA"
	if _, err := b2.Open(bad); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	// 另一把密钥打不开
	b3, _ := LoadOrCreate(filepath.Join(t.TempDir(), "other.key"))
	if _, err := b3.Open(sealed); err == nil {
		t.Fatal("opened with a different key")
	}
}
