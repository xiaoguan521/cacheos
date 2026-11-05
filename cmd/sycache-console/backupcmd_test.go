package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func TestBackupRestoreCLI(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	st, err := store.Open(filepath.Join(src, "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Create(&store.NodeGroup{Name: "default"})
	os.MkdirAll(filepath.Join(src, "pki"), 0o700)
	os.WriteFile(filepath.Join(src, "pki", "ca.key"), []byte("K"), 0o600)
	pf := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(pf, []byte("long enough passphrase\n"), 0o600)
	out := filepath.Join(t.TempDir(), "b.etosbk")
	var so, se bytes.Buffer

	if rc := backupMain([]string{"backup", "-data", src, "-out", out}, &so, &se); rc == 0 || !strings.Contains(se.String(), "ETOS_BACKUP_PASSPHRASE") {
		t.Fatalf("backup without passphrase: rc=%d %s", rc, se.String())
	}
	short := filepath.Join(t.TempDir(), "short")
	os.WriteFile(short, []byte("short"), 0o600)
	se.Reset()
	if rc := backupMain([]string{"backup", "-data", src, "-out", out, "-passphrase-file", short}, &so, &se); rc == 0 {
		t.Fatal("short passphrase accepted")
	}
	se.Reset()
	if rc := backupMain([]string{"backup", "-data", src, "-out", out, "-passphrase-file", pf}, &so, &se); rc != 0 {
		t.Fatalf("backup: %s", se.String())
	}
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", fi, err)
	}
	if _, err := os.Stat(out + ".partial"); err == nil {
		t.Fatal("partial file left behind")
	}
	if rc := backupMain([]string{"restore", "-data", dst, "-in", out, "-passphrase-file", pf}, &so, &se); rc != 0 {
		t.Fatalf("restore: %s", se.String())
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "pki", "ca.key")); string(b) != "K" {
		t.Fatal("ca.key not restored")
	}
	rst, _ := store.Open(filepath.Join(dst, "console.db"))
	var n int64
	rst.DB.Model(&store.AuditLog{}).Where("action = ?", "system.restore").Count(&n)
	if n != 1 {
		t.Fatal("restore not audited")
	}
	// 再次恢复到同一目标：非空库，拒绝
	se.Reset()
	if rc := backupMain([]string{"restore", "-data", dst, "-in", out, "-passphrase-file", pf, "-force"}, &so, &se); rc == 0 || !strings.Contains(se.String(), "不是空库") {
		t.Fatalf("second restore: rc=%d %s", rc, se.String())
	}
}
