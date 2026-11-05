// Package audit 实现只增的审计日志，每条记录包含前一条的哈希，形成哈希链（规格书 7.1 M10、9.3）。
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
)

// Logger 串行写入审计记录。
type Logger struct {
	DB *gorm.DB
	mu sync.Mutex
}

// Entry 是一条待写入的审计事件。
type Entry struct {
	Username, IP, Action, ObjectType, ObjectID, Result string
	Detail                                             any
}

func hashOf(r *store.AuditLog) string {
	s := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s", r.TS.UTC().Format(time.RFC3339Nano), r.Username, r.IP,
		r.Action, r.ObjectType, r.ObjectID, r.Detail, r.Result, r.PrevHash)
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Write 追加一条记录。
func (l *Logger) Write(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	detail := ""
	if e.Detail != nil {
		b, _ := json.Marshal(e.Detail)
		detail = string(b)
	}
	if e.Result == "" {
		e.Result = "success"
	}
	a := query.Use(l.DB).AuditLog
	ctx := context.Background()
	prev := ""
	if last, err := a.WithContext(ctx).Order(a.ID.Desc()).Limit(1).Find(); err != nil {
		return err
	} else if len(last) == 1 {
		prev = last[0].Hash
	}
	r := &store.AuditLog{TS: time.Now().UTC().Truncate(time.Microsecond), Username: e.Username, IP: e.IP, Action: e.Action,
		ObjectType: e.ObjectType, ObjectID: e.ObjectID, Detail: detail, Result: e.Result, PrevHash: prev}
	r.Hash = hashOf(r)
	return a.WithContext(ctx).Create(r)
}

// Verify 校验整条哈希链，返回第一条被篡改记录的 ID（0 表示完好）。
func (l *Logger) Verify() (uint, error) {
	a := query.Use(l.DB).AuditLog
	rows, err := a.WithContext(context.Background()).Order(a.ID).Find()
	if err != nil {
		return 0, err
	}
	prev := ""
	for _, r := range rows {
		if r.PrevHash != prev || hashOf(r) != r.Hash {
			return r.ID, nil
		}
		prev = r.Hash
	}
	return 0, nil
}
