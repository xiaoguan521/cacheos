package store_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
)

// schemaOf 用 gorm Migrator 读出每张业务表的列（名称、类型、可空、主键、唯一）与索引，SQLite / PostgreSQL 通用。
func schemaOf(t *testing.T, db *gorm.DB) string {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(tables)
	var b strings.Builder
	for _, tbl := range tables {
		if tbl == "migrations" {
			continue
		}
		cols, err := db.Migrator().ColumnTypes(tbl)
		if err != nil {
			t.Fatal(err)
		}
		var cs []string
		for _, c := range cols {
			nullable, _ := c.Nullable()
			pk, _ := c.PrimaryKey()
			uniq, _ := c.Unique()
			n, _ := c.Length()
			cs = append(cs, fmt.Sprintf("%s %s(%d) null=%v pk=%v uniq=%v", c.Name(), strings.ToLower(c.DatabaseTypeName()), n, nullable, pk, uniq))
		}
		sort.Strings(cs)
		idx, err := db.Migrator().GetIndexes(tbl)
		if err != nil {
			t.Fatal(err)
		}
		var is []string
		for _, i := range idx {
			u, _ := i.Unique()
			p, _ := i.PrimaryKey()
			if p {
				continue
			}
			is = append(is, fmt.Sprintf("%s(%s) unique=%v", i.Name(), strings.Join(i.Columns(), ","), u))
		}
		sort.Strings(is)
		fmt.Fprintf(&b, "%s\n  %s\n  idx: %s\n", tbl, strings.Join(cs, "\n  "), strings.Join(is, "; "))
	}
	return b.String()
}

// 修改 model.go 却没有新增迁移时失败：全部迁移后的表结构必须与当前模型直接建表的结果一致。
func TestModelsMatchMigrations(t *testing.T) {
	migrated := storetest.OpenRaw(t)
	if err := store.Migrate(migrated); err != nil {
		t.Fatal(err)
	}
	fromModels := storetest.OpenRaw(t)
	if err := fromModels.AutoMigrate(store.Models()...); err != nil {
		t.Fatal(err)
	}
	if a, b := schemaOf(t, migrated), schemaOf(t, fromModels); a != b {
		t.Fatalf("model.go 与迁移不一致：请在 migrations.go 末尾新增一条迁移。\n--- 迁移结果\n%s\n--- 当前模型\n%s", a, b)
	}
	// 迁移之后再对当前模型 AutoMigrate 不应再有任何变化
	if err := migrated.AutoMigrate(store.Models()...); err != nil {
		t.Fatal(err)
	}
	if a, b := schemaOf(t, migrated), schemaOf(t, fromModels); a != b {
		t.Fatal("AutoMigrate after migrations still changed the schema")
	}
}

// 从任意一个历史迁移点升级到最新，结果都与全新安装相同。
func TestUpgradeFromEveryMigration(t *testing.T) {
	fresh := storetest.OpenRaw(t)
	if err := store.Migrate(fresh); err != nil {
		t.Fatal(err)
	}
	want := schemaOf(t, fresh)
	for _, id := range store.MigrationIDs() {
		db := storetest.OpenRaw(t)
		if err := store.MigrateTo(db, id); err != nil {
			t.Fatalf("migrate to %s: %v", id, err)
		}
		if err := store.Migrate(db); err != nil {
			t.Fatalf("upgrade from %s: %v", id, err)
		}
		if got := schemaOf(t, db); got != want {
			t.Fatalf("upgrade from %s differs from a fresh install\n--- upgraded\n%s\n--- fresh\n%s", id, got, want)
		}
		// 再次执行是空操作
		if err := store.Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
}

// 迁移 ID 唯一且按时间顺序排列（新迁移只能追加在末尾）。
func TestMigrationIDsOrdered(t *testing.T) {
	ids := store.MigrationIDs()
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("migration %s must sort after %s", ids[i], ids[i-1])
		}
	}
}
