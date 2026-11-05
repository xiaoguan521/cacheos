// Package backup 实现控制台的加密备份与恢复（规格书 P6-6）。
//
// 备份是逻辑备份：逐表导出（gob 编码，保留主键与二进制字段）加上数据目录中的密钥文件
// （节点 CA、私钥加密密钥、安装 ID）。因此同一份备份可以恢复到 SQLite 或 PostgreSQL，
// 也是 SQLite 迁移到 PostgreSQL 的方式。备份包含 CA 私钥，必须加密：Argon2id 派生密钥 + AES-256-GCM。
//
// 文件格式：magic(8) | salt(16) | nonce(12) | AES-GCM(tar.gz)
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/version"
)

const magic = "ETOSBK1\n"

// 数据目录中需要随备份保存的文件（web-tls 可重新生成，不备份）。
var dataFiles = []string{"installation.json", "secret.key", "pki/ca.crt", "pki/ca.key"}

// Options 控制备份内容。
type Options struct {
	Metrics bool // 是否包含流量统计（通常很大，默认不包含）
}

// Summary 描述一份备份。
type Summary struct {
	Product   string           `json:"product"`
	Version   string           `json:"version"`
	CreatedAt time.Time        `json:"created_at"`
	Tables    map[string]int64 `json:"tables"`
	Files     []string         `json:"files"`
}

// table 把一张表的导出 / 导入绑定到具体的模型类型（gob 需要具体类型）。
type table struct {
	name  string
	model any
	dump  func(db *gorm.DB, w io.Writer) (int64, error)
	load  func(db *gorm.DB, r io.Reader) (int64, error)
}

func tbl[T any](name string) table {
	return table{
		name:  name,
		model: new(T),
		dump: func(db *gorm.DB, w io.Writer) (int64, error) {
			enc := gob.NewEncoder(w)
			var n int64
			var batch []T
			res := db.Model(new(T)).FindInBatches(&batch, 500, func(tx *gorm.DB, _ int) error {
				n += int64(len(batch))
				return enc.Encode(batch)
			})
			return n, res.Error
		},
		load: func(db *gorm.DB, r io.Reader) (int64, error) {
			dec := gob.NewDecoder(r)
			var n int64
			for {
				var batch []T
				if err := dec.Decode(&batch); err == io.EOF {
					return n, nil
				} else if err != nil {
					return n, err
				}
				if len(batch) == 0 {
					continue
				}
				// 关联字段（如 Deployment.Nodes）单独成表导入，这里不级联
				if err := db.Session(&gorm.Session{FullSaveAssociations: false}).Omit(clauseAssociations).CreateInBatches(batch, 200).Error; err != nil {
					return n, err
				}
				n += int64(len(batch))
			}
		},
	}
}

const clauseAssociations = "*.*" // gorm: Omit 所有关联

// tables 按依赖顺序列出全部业务表；分钟指标是可再生数据，同样备份以便恢复后保留历史曲线。
func tables() []table {
	return []table{
		tbl[store.SystemKV]("system_kvs"),
		tbl[store.User]("users"),
		tbl[store.Host]("hosts"),
		tbl[store.EnrollToken]("enroll_tokens"),
		tbl[store.ParamTemplate]("param_templates"),
		tbl[store.Unit]("units"),
		tbl[store.Instance]("instances"),
		tbl[store.ParamRevision]("param_revisions"),
		tbl[store.Operation]("operations"),
		tbl[store.AclAccount]("acl_accounts"),
		tbl[store.Backup]("backups"),
		tbl[store.BackupPolicy]("backup_policies"),
		tbl[store.MigrationJob]("migration_jobs"),
		tbl[store.EnginePackage]("engine_packages"),
		tbl[store.APIToken]("api_tokens"),
		tbl[store.AlertChannel]("alert_channels"),
		tbl[store.AlertRule]("alert_rules"),
		tbl[store.AlertEvent]("alert_events"),
		tbl[store.AuditLog]("audit_logs"),
		tbl[store.LicenseStore]("license_stores"),
		tbl[store.MetricMinute]("metric_minutes"),
	}
}

// 备份口令派生参数（Argon2id：3 轮、64 MiB、4 线程）。文件格式 ETOSBK1 固定使用这组参数；
// 只有本包的测试会临时调低它们（见 TestMain），生产代码没有任何开关可以改变。
var kdfTime, kdfMemoryKiB, kdfThreads uint32 = 3, 64 * 1024, 4

func deriveKey(pass, salt []byte) []byte {
	return argon2.IDKey(pass, salt, kdfTime, kdfMemoryKiB, uint8(kdfThreads), 32)
}

// Create 写出一份加密备份。
func Create(w io.Writer, st *store.Store, dataDir string, pass []byte, opt Options) (*Summary, error) {
	if len(pass) < 8 {
		return nil, errors.New("备份口令至少 8 个字符")
	}
	sum := &Summary{Product: version.Product, Version: version.Version, CreatedAt: time.Now().UTC(), Tables: map[string]int64{}}
	var plain bytes.Buffer
	gz := gzip.NewWriter(&plain)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: sum.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	// 所有表在同一个只读事务里读取，得到一致的快照（不会出现版本已写入、发布记录还没有的半截状态）。
	// PostgreSQL 默认 READ COMMITTED 每条语句各看一次，需要 REPEATABLE READ。
	txOpt := &sql.TxOptions{}
	if st.DB.Dialector.Name() == "postgres" {
		txOpt = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	err := st.DB.Transaction(func(tx *gorm.DB) error {
		for _, t := range tables() {
			if t.name == "metric_minutes" && !opt.Metrics {
				sum.Tables[t.name] = 0
				continue
			}
			var b bytes.Buffer
			n, err := t.dump(tx, &b)
			if err != nil {
				return fmt.Errorf("export %s: %w", t.name, err)
			}
			sum.Tables[t.name] = n
			if err := add("db/"+t.name+".gob", b.Bytes(), 0o600); err != nil {
				return err
			}
		}
		return nil
	}, txOpt)
	if err != nil {
		return nil, err
	}
	for _, f := range dataFiles {
		b, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(f)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := add("files/"+f, b, 0o600); err != nil {
			return nil, err
		}
		sum.Files = append(sum.Files, f)
	}
	mj, _ := json.MarshalIndent(sum, "", "  ")
	if err := add("manifest.json", mj, 0o644); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	salt, nonce := make([]byte, 16), make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(deriveKey(pass, salt))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	header := append(append([]byte(magic), salt...), nonce...)
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	if _, err := w.Write(aead.Seal(nil, nonce, plain.Bytes(), header)); err != nil {
		return nil, err
	}
	return sum, nil
}

// Restore 把备份恢复到空库与数据目录。force 为 true 时覆盖数据目录中已有的密钥文件。
func Restore(r io.Reader, st *store.Store, dataDir string, pass []byte, force bool) (*Summary, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<30))
	if err != nil {
		return nil, err
	}
	if len(raw) < len(magic)+28 || string(raw[:len(magic)]) != magic {
		return nil, errors.New("不是 EdgeTraffic OS 备份文件")
	}
	header := raw[:len(magic)+28]
	salt, nonce := header[len(magic):len(magic)+16], header[len(magic)+16:]
	blk, err := aes.NewCipher(deriveKey(pass, salt))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, raw[len(header):], header)
	if err != nil {
		return nil, errors.New("无法解密：口令错误或备份文件已损坏")
	}
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, err
	}
	entries := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		clean := path.Clean(h.Name)
		if h.Typeflag != tar.TypeReg || clean != h.Name || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
			return nil, fmt.Errorf("备份中有不安全的路径：%s", h.Name)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		entries[clean] = b
	}
	var sum Summary
	if err := json.Unmarshal(entries["manifest.json"], &sum); err != nil {
		return nil, errors.New("备份缺少清单")
	}

	// 目标必须是空库：恢复会保留原有主键，不能与已有数据混在一起
	// 只启动过、未初始化的控制台会在 system_kvs 里生成启动密钥，恢复时以备份为准覆盖。
	for _, t := range tables() {
		if t.name == "system_kvs" {
			continue
		}
		var n int64
		if err := st.DB.Model(t.model).Count(&n).Error; err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fmt.Errorf("目标数据库不是空库（%s 有 %d 行）：请恢复到新建的数据库", t.name, n)
		}
	}
	if !force {
		for _, f := range dataFiles {
			if _, err := os.Stat(filepath.Join(dataDir, filepath.FromSlash(f))); err == nil {
				if _, inBackup := entries["files/"+f]; inBackup {
					return nil, fmt.Errorf("数据目录中已有 %s；确认要覆盖时加 -force", f)
				}
			}
		}
	}

	err = st.DB.Transaction(func(tx *gorm.DB) error {
		if _, ok := entries["db/system_kvs.gob"]; ok {
			if err := tx.Where("1 = 1").Delete(&store.SystemKV{}).Error; err != nil {
				return err
			}
		}
		for _, t := range tables() {
			b, ok := entries["db/"+t.name+".gob"]
			if !ok {
				continue
			}
			if _, err := t.load(tx, bytes.NewReader(b)); err != nil {
				return fmt.Errorf("import %s: %w", t.name, err)
			}
		}
		return resetSequences(tx)
	})
	if err != nil {
		return nil, err
	}
	for _, f := range dataFiles {
		b, ok := entries["files/"+f]
		if !ok {
			continue
		}
		p := filepath.Join(dataDir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		mode := os.FileMode(0o600)
		if strings.HasSuffix(f, ".crt") {
			mode = 0o644
		}
		if err := os.WriteFile(p, b, mode); err != nil {
			return nil, err
		}
	}
	return &sum, nil
}

// resetSequences：PostgreSQL 的自增序列不会因为显式写入主键而前进，恢复后需要对齐到最大值。
func resetSequences(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, t := range tables() {
		stmt := &gorm.Statement{DB: tx}
		if err := stmt.Parse(t.model); err != nil {
			return err
		}
		pk := stmt.Schema.PrioritizedPrimaryField
		if pk == nil || pk.DataType == schema.String || !pk.AutoIncrement {
			continue
		}
		q := fmt.Sprintf("SELECT setval(pg_get_serial_sequence(?, ?), COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false)",
			tx.Statement.Quote(pk.DBName), tx.Statement.Quote(stmt.Schema.Table))
		if err := tx.Exec(q, stmt.Schema.Table, pk.DBName).Error; err != nil {
			return fmt.Errorf("reset sequence %s: %w", stmt.Schema.Table, err)
		}
	}
	return nil
}
