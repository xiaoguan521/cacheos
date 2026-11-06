package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalRoundTrip(t *testing.T) {
	root := t.TempDir()
	st, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, ".upload")
	data := []byte("RDB\x00test")
	sum := sha256.Sum256(data)
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := st.PutFile(context.Background(), "b-local", src, int64(len(data)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.Open(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(read) != string(data) {
		t.Fatalf("read back %q, err=%v", read, err)
	}
	if err := st.Delete(context.Background(), got); err != nil {
		t.Fatal(err)
	}
}

func TestS3RoundTripUsesSigV4AndStreamsFile(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("X-Amz-Date") == "" {
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/cacheos/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			objects[key] = data
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	st, err := NewS3(S3Config{Endpoint: server.URL, Bucket: "cacheos", Region: "test-1", AccessKey: "access", SecretKey: "secret", AllowHTTP: true, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("RDB\x00s3")
	sum := sha256.Sum256(data)
	src := filepath.Join(t.TempDir(), "backup.rdb")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := st.PutFile(context.Background(), "b-s3", src, int64(len(data)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if ref != "s3://cacheos/b-s3.rdb" {
		t.Fatalf("reference = %q", ref)
	}
	f, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(read) != string(data) {
		t.Fatalf("read back %q, err=%v", read, err)
	}
	if err := st.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(objects) != 0 {
		t.Fatalf("objects left after delete: %v", objects)
	}
}
