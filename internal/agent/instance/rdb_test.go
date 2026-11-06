package instance

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rdbString(s string) []byte {
	return append([]byte{byte(len(s))}, []byte(s)...)
}

func TestAnalyzeRDBMetadataOnly(t *testing.T) {
	var b []byte
	b = append(b, []byte("REDIS0011")...)
	// Database 0, one expiring string key, and one hash key.
	b = append(b, 0xFE, 0x00)
	b = append(b, 0xFC)
	exp := make([]byte, 8)
	binary.LittleEndian.PutUint64(exp, uint64(time.Now().Add(time.Hour).UnixMilli()))
	b = append(b, exp...)
	b = append(b, 0x00) // string
	b = append(b, rdbString("cache:a")...)
	b = append(b, rdbString("value")...)
	b = append(b, 0x04) // hash
	b = append(b, rdbString("cache:h")...)
	b = append(b, 0x01)
	b = append(b, rdbString("field")...)
	b = append(b, rdbString("value")...)
	b = append(b, 0xFF)
	b = append(b, make([]byte, 8)...)

	path := filepath.Join(t.TempDir(), "dump.rdb")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := analyzeRDB(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.RDBVersion != "0011" || report.Keys != 2 || report.ExpiringKeys != 1 || report.Databases["0"] != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Types["string"] != 1 || report.Types["hash"] != 1 || len(report.TopKeys) != 2 {
		t.Fatalf("unexpected type report: %+v", report)
	}
}
