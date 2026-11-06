package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestS3CompatibleRoundTrip is intentionally opt-in: normal unit-test runs
// must not require Docker or an object-storage service. CI enables it with a
// disposable S3-compatible service, so the production S3 configuration is
// exercised end to end.
func TestS3CompatibleRoundTrip(t *testing.T) {
	endpoint := os.Getenv("CACHEOS_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("CACHEOS_S3_ENDPOINT is not set")
	}
	allowHTTP := false
	if raw := os.Getenv("CACHEOS_S3_ALLOW_HTTP"); raw != "" {
		var err error
		allowHTTP, err = strconv.ParseBool(raw)
		if err != nil {
			t.Fatalf("CACHEOS_S3_ALLOW_HTTP: %v", err)
		}
	}
	bucket := envOr("CACHEOS_S3_BUCKET", "cacheos")
	region := envOr("CACHEOS_S3_REGION", "us-east-1")
	accessKey := envOr("CACHEOS_S3_ACCESS_KEY", "cacheos")
	secretKey := envOr("CACHEOS_S3_SECRET_KEY", "cacheos-secret-2026")

	st, err := NewS3(S3Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		Region:    region,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Prefix:    "cacheos-integration",
		PathStyle: true,
		AllowHTTP: allowHTTP,
		StageDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("REDIS\x00CACHEOS-S3-ROUNDTRIP\n")
	sum := sha256.Sum256(data)
	src := filepath.Join(t.TempDir(), "backup.rdb")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	id := "b-s3-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ref, err := st.PutFile(context.Background(), id, src, int64(len(data)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Delete(context.Background(), ref); err != nil {
			t.Errorf("delete S3 object: %v", err)
		}
	})

	f, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip data = %q, want %q", got, data)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
