package instance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreBackupStreamVerifiesBeforePublish(t *testing.T) {
	root := t.TempDir()
	m := &Manager{L: Layout{StateDir: filepath.Join(root, "state")}}
	id := "b-stream"
	data := bytes.Repeat([]byte("rdb"), 1024)
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	if err := m.StoreBackupStream(context.Background(), id, want, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := m.L.BackupPath(id)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored backup differs: got %d bytes, want %d", len(got), len(data))
	}

	err = m.StoreBackupStream(context.Background(), id, "bad-hash", func(w io.Writer) error {
		_, err := w.Write([]byte("corrupt"))
		return err
	})
	if err == nil {
		t.Fatal("bad hash was accepted")
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("hash failure replaced the previously published backup")
	}
}
