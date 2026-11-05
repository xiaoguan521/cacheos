// Package store 是控制台的数据层（规格书 10.1，MVP 子集）。
//
// MVP 阶段在本包内集中封装全部 GORM 查询（均为参数化查询，禁止拼接 SQL）；
// 正式版按 CLAUDE.md 迁移到 gorm/gen 生成的类型安全查询。
package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Store 包装数据库连接。
type Store struct{ DB *gorm.DB }

// Open 打开数据库并升级到最新版本：dsn 以 postgres:// 开头时使用 PostgreSQL，否则视为 SQLite 文件路径。
func Open(dsn string) (*Store, error) {
	db, err := OpenDB(dsn)
	if err != nil {
		return nil, err
	}
	if err := migrateLocked(db, dsn); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{DB: db}, nil
}

// migrateLockKey 串行化多台控制台（高可用）同时启动时的迁移（"ETOSMIG1"）。
const migrateLockKey int64 = 0x45544f534d494731

// migrateLocked 在 PostgreSQL 上持有 advisory lock 执行迁移，避免两台控制台并发迁移；SQLite 直接迁移。
func migrateLocked(db *gorm.DB, dsn string) error {
	if !IsPostgres(dsn) {
		return Migrate(db)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrateLockKey) //nolint:errcheck
	return Migrate(db)
}

// IsPostgres 报告 dsn 是否指向 PostgreSQL。
func IsPostgres(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// OpenDB 只建立连接，不执行迁移（测试升级路径时使用）。
func OpenDB(dsn string) (*gorm.DB, error) {
	var dial gorm.Dialector
	if IsPostgres(dsn) {
		dial = postgres.Open(dsn)
	} else {
		dial = sqlite.Open(dsn + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	}
	db, err := gorm.Open(dial, &gorm.Config{Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
		SlowThreshold: time.Second, LogLevel: logger.Warn, IgnoreRecordNotFoundError: true})})
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil && !IsPostgres(dsn) {
		sqlDB.SetMaxOpenConns(1) // SQLite 单写者
	}
	return db, nil
}

// ErrNotFound 统一表示记录不存在。
var ErrNotFound = errors.New("not found")

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// GetKV / SetKV 读写系统键值。
func (s *Store) GetKV(key string) (string, error) {
	var kv SystemKV
	if err := s.DB.Where("key = ?", key).First(&kv).Error; err != nil {
		return "", notFound(err)
	}
	return kv.Value, nil
}

func (s *Store) SetKV(key, value string) error {
	return s.DB.Save(&SystemKV{Key: key, Value: value}).Error
}

// InitKV 在键不存在时写入 value，返回库中最终的值（并发启动的多台控制台得到同一个值）。
func (s *Store) InitKV(key, value string) (string, error) {
	if err := s.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&SystemKV{Key: key, Value: value}).Error; err != nil {
		return "", err
	}
	return s.GetKV(key)
}

// First 按主键取一条记录。
func First[T any](s *Store, id any) (*T, error) {
	var v T
	if err := s.DB.First(&v, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}
