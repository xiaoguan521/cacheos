package api

import (
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
)

// 迁移作业的行读取失败（被删除或数据库抖动）时，部署单元的变更锁仍必须释放，
// 否则该单元会一直显示“有变更进行中”，直到人工清库。
func TestFinishMigrationReleasesUnitLockWhenJobRowIsGone(t *testing.T) {
	st := storetest.Open(t)
	s := &Server{Store: st}
	if err := st.LockUnit(7, 42); err != nil {
		t.Fatal(err)
	}
	s.finishMigration(7, 42, "failed", "{}", "boom")
	if owner, err := st.UnitLockOwner(7); err != nil || owner != 0 {
		t.Fatalf("unit lock owner = %d (err %v), want released", owner, err)
	}
}

// 作业行读不到时 runMigration 也不能把锁留在原地。
func TestRunMigrationReleasesUnitLockWhenJobRowIsGone(t *testing.T) {
	st := storetest.Open(t)
	s := &Server{Store: st}
	if err := st.LockUnit(7, 42); err != nil {
		t.Fatal(err)
	}
	s.runMigration(7, 42, 1, 6379)
	if owner, err := st.UnitLockOwner(7); err != nil || owner != 0 {
		t.Fatalf("unit lock owner = %d (err %v), want released", owner, err)
	}
}
