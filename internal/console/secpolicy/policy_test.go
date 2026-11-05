package secpolicy

import (
	"testing"
	"time"

	"github.com/xiaoguan521/cacheos/internal/pkg/crypto"
)

func TestCheckPassword(t *testing.T) {
	p := Default
	for pw, ok := range map[string]bool{
		"short1A!":          false, // 太短
		"alllowercase123":   false, // 只有两类
		"Abcdefgh12":        true,
		"admin-Strong#2026": false, // 含用户名
		"Zx9!zx9!zx9!":      true,
	} {
		if err := p.CheckPassword("admin", pw); (err == nil) != ok {
			t.Errorf("%q: err=%v want ok=%v", pw, err, ok)
		}
	}
}

func TestHistoryAndExpiry(t *testing.T) {
	h1, _ := crypto.HashPassword("Old#Pass2024")
	h2, _ := crypto.HashPassword("Cur#Pass2025")
	hist := PushHistory("", h1, 5)
	if !InHistory("Old#Pass2024", h2, hist) || !InHistory("Cur#Pass2025", h2, hist) || InHistory("New#Pass2026", h2, hist) {
		t.Fatal("history check")
	}
	hist = PushHistory(hist, h2, 1)
	if InHistory("Old#Pass2024", "", hist) {
		t.Fatal("history should be trimmed to 1")
	}
	now := time.Now()
	old := now.Add(-100 * 24 * time.Hour)
	if !Default.Expired(&old, old, now) || Default.Expired(nil, now, now) {
		t.Fatal("expiry")
	}
	if Parse(`{"min_length":3}`) != Default {
		t.Fatal("invalid stored policy must fall back to default")
	}
}
