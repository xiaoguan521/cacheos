// Package secpolicy 是控制台账号安全策略（等保 2.0 三级：身份鉴别与访问控制，规格书 12.3 P9）。
package secpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/xiaoguan521/cacheos/internal/pkg/crypto"
)

// Policy 是可配置的安全策略。
type Policy struct {
	MinLength     int `json:"min_length"`     // 口令最短长度
	MinClasses    int `json:"min_classes"`    // 至少包含大写、小写、数字、符号中的几类
	ExpireDays    int `json:"expire_days"`    // 口令有效期（0 表示不过期）
	History       int `json:"history"`        // 不得与最近几次口令相同
	LockThreshold int `json:"lock_threshold"` // 连续失败几次锁定账号
	LockMinutes   int `json:"lock_minutes"`   // 锁定时长
	IdleMinutes   int `json:"idle_minutes"`   // 会话空闲超时
	MaxHours      int `json:"max_hours"`      // 会话最长有效期
}

// Default 符合等保 2.0 三级的常见要求。
var Default = Policy{MinLength: 10, MinClasses: 3, ExpireDays: 90, History: 5, LockThreshold: 5, LockMinutes: 30, IdleMinutes: 30, MaxHours: 8}

// Validate 校验策略本身的取值范围。
func (p Policy) Validate() error {
	switch {
	case p.MinLength < 8 || p.MinLength > 64:
		return errors.New("口令最短长度应在 8–64 之间")
	case p.MinClasses < 1 || p.MinClasses > 4:
		return errors.New("字符类别数应在 1–4 之间")
	case p.ExpireDays < 0 || p.ExpireDays > 3650:
		return errors.New("口令有效期应在 0–3650 天之间")
	case p.History < 0 || p.History > 24:
		return errors.New("历史口令数应在 0–24 之间")
	case p.LockThreshold < 3 || p.LockThreshold > 20:
		return errors.New("锁定阈值应在 3–20 次之间")
	case p.LockMinutes < 1 || p.LockMinutes > 1440:
		return errors.New("锁定时长应在 1–1440 分钟之间")
	case p.IdleMinutes < 5 || p.IdleMinutes > 480:
		return errors.New("空闲超时应在 5–480 分钟之间")
	case p.MaxHours < 1 || p.MaxHours > 24:
		return errors.New("会话最长有效期应在 1–24 小时之间")
	}
	return nil
}

// Parse 读取保存的策略，缺失字段用默认值。
func Parse(s string) Policy {
	p := Default
	if s != "" {
		_ = json.Unmarshal([]byte(s), &p)
	}
	if p.Validate() != nil {
		return Default
	}
	return p
}

// CheckPassword 按策略检查新口令（不检查历史）。
func (p Policy) CheckPassword(username, pw string) error {
	if len([]rune(pw)) < p.MinLength {
		return fmt.Errorf("口令至少 %d 个字符", p.MinLength)
	}
	var up, lo, di, sy bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			lo = true
		case unicode.IsDigit(r):
			di = true
		default:
			sy = true
		}
	}
	n := 0
	for _, b := range []bool{up, lo, di, sy} {
		if b {
			n++
		}
	}
	if n < p.MinClasses {
		return fmt.Errorf("口令需包含大写字母、小写字母、数字、符号中的至少 %d 类", p.MinClasses)
	}
	if username != "" && strings.Contains(strings.ToLower(pw), strings.ToLower(username)) {
		return errors.New("口令不能包含用户名")
	}
	return nil
}

// InHistory 报告口令是否与当前或历史口令相同。
func InHistory(pw, current, historyJSON string) bool {
	if current != "" && crypto.CheckPassword(current, pw) {
		return true
	}
	var hs []string
	_ = json.Unmarshal([]byte(historyJSON), &hs)
	for _, h := range hs {
		if crypto.CheckPassword(h, pw) {
			return true
		}
	}
	return false
}

// PushHistory 把旧口令哈希加入历史，保留最近 n 个。
func PushHistory(historyJSON, oldHash string, n int) string {
	var hs []string
	_ = json.Unmarshal([]byte(historyJSON), &hs)
	if oldHash != "" {
		hs = append([]string{oldHash}, hs...)
	}
	if len(hs) > n {
		hs = hs[:n]
	}
	b, _ := json.Marshal(hs)
	return string(b)
}

// Expired 报告口令是否已过有效期。
func (p Policy) Expired(changed *time.Time, created time.Time, now time.Time) bool {
	if p.ExpireDays == 0 {
		return false
	}
	t := created
	if changed != nil {
		t = *changed
	}
	return now.Sub(t) > time.Duration(p.ExpireDays)*24*time.Hour
}
