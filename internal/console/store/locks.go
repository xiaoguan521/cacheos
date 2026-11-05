package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrUnitBusy 表示部署单元已有进行中的变更（规格书 8 节：同一部署单元同时只允许一个变更）。
var ErrUnitBusy = errors.New("unit has an operation in progress")

// LockUnit 为部署单元加变更锁：插入 unit_locks 行，主键冲突即说明已被占用。
// 多台控制台（高可用）共享同一数据库，因此用数据库唯一约束而不是进程内锁。
func (s *Store) LockUnit(unitID, operationID uint) error {
	res := s.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&UnitLock{UnitID: unitID, OperationID: operationID, LockedAt: time.Now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUnitBusy
	}
	return nil
}

// UnlockUnit 释放变更锁；只释放属于该变更的锁。
func (s *Store) UnlockUnit(unitID, operationID uint) error {
	return s.DB.Where("unit_id = ? AND operation_id = ?", unitID, operationID).Delete(&UnitLock{}).Error
}

// UnitLockOwner 返回持有锁的变更 ID（0 表示未加锁）。
func (s *Store) UnitLockOwner(unitID uint) (uint, error) {
	var l UnitLock
	err := s.DB.First(&l, "unit_id = ?", unitID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return l.OperationID, err
}

// NextRevisionID 在事务中生成下一个参数版本号（p + 8 位序号，按全局自增序号）。
func NextRevisionID(tx *gorm.DB) (string, error) {
	var last ParamRevision
	err := tx.Order("seq DESC").Limit(1).Find(&last).Error
	if err != nil {
		return "", err
	}
	return formatRev(last.Seq + 1), nil
}

func formatRev(n uint) string {
	const digits = "0123456789"
	b := []byte("p00000000")
	for i := len(b) - 1; i > 0 && n > 0; i-- {
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b)
}
