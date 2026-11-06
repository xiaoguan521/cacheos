package instance

// 本文件实现一个只读的 RDB 目录分析器。它只读取 agent 本机实例目录中的
// RDB，不连接数据面、不读取业务 Key 的 value，并把结果限制为统计信息和
// 少量大对象候选，符合规格书 M7 的“离线分析不上传数据”约束。

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type rdbReader struct {
	r *bufio.Reader
	n int64
}

func newRDBReader(rd io.Reader) *rdbReader {
	return &rdbReader{r: bufio.NewReaderSize(rd, 64<<10)}
}

func (r *rdbReader) readByte() (byte, error) {
	b, err := r.r.ReadByte()
	if err == nil {
		r.n++
	}
	return b, err
}

func (r *rdbReader) readBytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("negative read length")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r.r, b); err != nil {
		return nil, err
	}
	r.n += int64(n)
	return b, nil
}

func (r *rdbReader) skip(n uint64) error {
	if n > uint64(^uint(0)>>1) {
		return fmt.Errorf("RDB field is too large")
	}
	_, err := io.CopyN(io.Discard, r.r, int64(n))
	if err == nil {
		r.n += int64(n)
	}
	return err
}

// readLengthEncodedLength returns (length, encoding, error). encoding is -1
// for a normal length and 0..3 for Redis's special string encodings.
func (r *rdbReader) readLengthEncodedLength() (uint64, int, error) {
	b, err := r.readByte()
	if err != nil {
		return 0, 0, err
	}
	switch b >> 6 {
	case 0:
		return uint64(b & 0x3f), -1, nil
	case 1:
		n, err := r.readByte()
		if err != nil {
			return 0, 0, err
		}
		return uint64(b&0x3f)<<8 | uint64(n), -1, nil
	case 2:
		p, err := r.readBytes(4)
		if err != nil {
			return 0, 0, err
		}
		return uint64(binary.LittleEndian.Uint32(p)), -1, nil
	default:
		return 0, int(b & 0x3f), nil
	}
}

func (r *rdbReader) readLength() (uint64, error) {
	n, enc, err := r.readLengthEncodedLength()
	if err != nil {
		return 0, err
	}
	if enc >= 0 {
		return 0, fmt.Errorf("special RDB encoding used where a length was required")
	}
	return n, nil
}

// readString skips one RDB string and returns its plain bytes when it is a
// normal length-prefixed string. Encoded integers/LZF values are skipped but
// intentionally not decoded because the analyzer only needs their size.
func (r *rdbReader) readString() ([]byte, error) {
	n, enc, err := r.readLengthEncodedLength()
	if err != nil {
		return nil, err
	}
	if enc < 0 {
		if n > uint64(64<<20) {
			return nil, fmt.Errorf("RDB string exceeds analyzer limit")
		}
		return r.readBytes(int(n))
	}
	switch enc {
	case 0: // 8-bit integer
		return r.readBytes(1)
	case 1: // 16-bit integer
		return r.readBytes(2)
	case 2: // 32-bit integer
		return r.readBytes(4)
	case 3: // LZF: compressed length, uncompressed length, payload
		compressed, err := r.readLength()
		if err != nil {
			return nil, err
		}
		if _, err := r.readLength(); err != nil { // uncompressed length
			return nil, err
		}
		if compressed > uint64(64<<20) {
			return nil, fmt.Errorf("compressed RDB string exceeds analyzer limit")
		}
		if err := r.skip(compressed); err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown RDB string encoding %d", enc)
	}
}

func (r *rdbReader) readDouble() error {
	n, err := r.readByte()
	if err != nil {
		return err
	}
	// 253/254/255 are NaN, +Inf and -Inf markers. Other values are the
	// length of the ASCII representation saved by the upstream engine.
	if n >= 253 {
		return nil
	}
	return r.skip(uint64(n))
}

func (r *rdbReader) readObject(typ byte) error {
	readStrings := func(n uint64) error {
		if n > 1<<32 {
			return fmt.Errorf("RDB object has too many elements")
		}
		for i := uint64(0); i < n; i++ {
			if _, err := r.readString(); err != nil {
				return err
			}
		}
		return nil
	}
	readBlob := func() error {
		_, err := r.readString()
		return err
	}

	switch typ {
	case 0: // string
		return readBlob()
	case 1, 2: // list, set
		n, err := r.readLength()
		if err != nil {
			return err
		}
		return readStrings(n)
	case 3: // sorted set
		n, err := r.readLength()
		if err != nil {
			return err
		}
		if n > 1<<32 {
			return fmt.Errorf("RDB sorted set has too many elements")
		}
		for i := uint64(0); i < n; i++ {
			if _, err := r.readString(); err != nil {
				return err
			}
			if err := r.readDouble(); err != nil {
				return err
			}
		}
		return nil
	case 4: // hash
		n, err := r.readLength()
		if err != nil {
			return err
		}
		return readStrings(n * 2)
	case 9, 10, 11, 12, 13: // zipmap/ziplist/intset/listpack encodings
		return readBlob()
	case 14: // quicklist
		n, err := r.readLength()
		if err != nil {
			return err
		}
		return readStrings(n)
	default:
		return fmt.Errorf("RDB object type %d is not supported by analyzer", typ)
	}
}

type rdbKeyReport struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Bytes int64  `json:"bytes"`
	TTLMS int64  `json:"ttl_ms,omitempty"`
}

// RDBReport intentionally contains no values. TopKeys only contains names,
// types and approximate serialized sizes, so it is safe to return through the
// control plane as a diagnostic report.
type RDBReport struct {
	RDBVersion   string            `json:"rdb_version"`
	GeneratedAt  time.Time         `json:"generated_at"`
	Keys         uint64            `json:"keys"`
	ExpiringKeys uint64            `json:"expiring_keys"`
	ExpiredKeys  uint64            `json:"expired_keys"`
	ApproxBytes  uint64            `json:"approx_bytes"`
	Databases    map[string]uint64 `json:"databases"`
	Types        map[string]uint64 `json:"types"`
	TopKeys      []rdbKeyReport    `json:"top_keys"`
	Warnings     []string          `json:"warnings,omitempty"`
}

func rdbTypeName(typ byte) string {
	switch typ {
	case 0:
		return "string"
	case 1:
		return "list"
	case 2:
		return "set"
	case 3:
		return "zset"
	case 4:
		return "hash"
	case 9:
		return "zipmap"
	case 10:
		return "ziplist"
	case 11:
		return "intset"
	case 12:
		return "zset-ziplist"
	case 13:
		return "hash-ziplist"
	case 14:
		return "quicklist"
	default:
		return fmt.Sprintf("type-%d", typ)
	}
}

func (r *RDBReport) addTop(item rdbKeyReport) {
	r.TopKeys = append(r.TopKeys, item)
	sort.Slice(r.TopKeys, func(i, j int) bool { return r.TopKeys[i].Bytes > r.TopKeys[j].Bytes })
	if len(r.TopKeys) > 20 {
		r.TopKeys = r.TopKeys[:20]
	}
}

func analyzeRDB(path string, now time.Time) (*RDBReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := newRDBReader(f)
	header, err := r.readBytes(9)
	if err != nil {
		return nil, fmt.Errorf("read RDB header: %w", err)
	}
	if !strings.HasPrefix(string(header), "REDIS") {
		return nil, fmt.Errorf("invalid RDB header")
	}
	report := &RDBReport{RDBVersion: string(header[5:]), GeneratedAt: now.UTC(), Databases: map[string]uint64{}, Types: map[string]uint64{}}
	var db uint64
	var expiresAt int64
	for {
		op, err := r.readByte()
		if err != nil {
			return nil, fmt.Errorf("read RDB opcode: %w", err)
		}
		switch op {
		case 0xFA: // AUX field
			if _, err := r.readString(); err != nil {
				return nil, err
			}
			if _, err := r.readString(); err != nil {
				return nil, err
			}
		case 0xFB: // RESIZEDB
			if _, err := r.readLength(); err != nil {
				return nil, err
			}
			if _, err := r.readLength(); err != nil {
				return nil, err
			}
		case 0xFC: // millisecond expiration for the next key
			p, err := r.readBytes(8)
			if err != nil {
				return nil, err
			}
			expiresAt = int64(binary.LittleEndian.Uint64(p))
		case 0xFD: // second expiration for the next key
			p, err := r.readBytes(4)
			if err != nil {
				return nil, err
			}
			expiresAt = int64(binary.LittleEndian.Uint32(p)) * 1000
		case 0xF6: // LFU frequency for the next key
			if _, err := r.readByte(); err != nil {
				return nil, err
			}
		case 0xF7: // LRU idle time for the next key
			if _, err := r.readLength(); err != nil {
				return nil, err
			}
		case 0xFE: // SELECTDB
			db, err = r.readLength()
			if err != nil {
				return nil, err
			}
		case 0xFF: // EOF + checksum
			_, _ = r.readBytes(8)
			return report, nil
		default:
			key, err := r.readString()
			if err != nil {
				return nil, fmt.Errorf("read key at offset %d: %w", r.n, err)
			}
			start := r.n
			if err := r.readObject(op); err != nil {
				return nil, fmt.Errorf("read %s value at offset %d: %w", rdbTypeName(op), start, err)
			}
			report.Keys++
			report.Databases[fmt.Sprint(db)]++
			name := rdbTypeName(op)
			report.Types[name]++
			recordBytes := int64(len(key)) + (r.n - start)
			if recordBytes > 0 {
				report.ApproxBytes += uint64(recordBytes)
			}
			ttl := int64(0)
			if expiresAt > 0 {
				report.ExpiringKeys++
				ttl = expiresAt - now.UnixMilli()
				if ttl <= 0 {
					report.ExpiredKeys++
				}
			}
			if len(key) > 128 {
				key = append(append([]byte(nil), key[:128]...), "…"...)
			}
			report.addTop(rdbKeyReport{Key: string(key), Type: name, Bytes: recordBytes, TTLMS: ttl})
			expiresAt = 0
		}
	}
}

func marshalRDBReport(path string, now time.Time) ([]byte, error) {
	report, err := analyzeRDB(path, now)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"kind": "RDB_ANALYZE", "report": report})
}
