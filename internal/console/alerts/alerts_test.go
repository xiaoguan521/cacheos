package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/secretbox"
)

type hook struct {
	mu   sync.Mutex
	reqs []map[string]any
	urls []string
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	h.mu.Lock()
	h.reqs = append(h.reqs, m)
	h.urls = append(h.urls, r.URL.String())
	h.mu.Unlock()
	_, _ = w.Write([]byte(`{"errcode":0}`))
}

func newSvc(t *testing.T, im bool) (*Service, *time.Time) {
	st := storetest.Open(t)
	box, err := secretbox.LoadOrCreate(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	return &Service{Store: st, Box: box, IMAllow: func() bool { return im }, Now: func() time.Time { return now }}, &now
}

func TestEvaluateFiresAndResolves(t *testing.T) {
	s, now := newSvc(t, true)
	h := &hook{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	ch, err := s.SaveChannel(0, ChannelInput{Name: "hook", Type: "webhook", URL: srv.URL, Enabled: true}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	q := query.Use(s.Store.DB)
	ctx := context.Background()
	seen := now.Add(-10 * time.Minute)
	_ = q.Host.WithContext(ctx).Create(&store.Host{Name: "cache-1", Status: "online", LastSeenAt: &seen})
	u := &store.Unit{Name: "orders", Mode: store.ModeReplication, Status: "running"}
	_ = q.Unit.WithContext(ctx).Create(u)
	_ = q.Instance.WithContext(ctx).Create(&store.Instance{UnitID: u.ID, HostID: 1, Port: 7001, Kind: "server", Role: "master", State: "running",
		UsedMemory: 900 << 20, MaxMemory: 1000 << 20})
	_ = q.Instance.WithContext(ctx).Create(&store.Instance{UnitID: u.ID, HostID: 1, Port: 7002, Kind: "server", Role: "slave", State: "running",
		LinkStatus: "down", MasterHost: "10.0.0.1", MasterPort: 7001})
	// 已停止的单元不告警
	stopped := &store.Unit{Name: "paused", Mode: store.ModeStandalone, Status: "stopped"}
	_ = q.Unit.WithContext(ctx).Create(stopped)
	_ = q.Instance.WithContext(ctx).Create(&store.Instance{UnitID: stopped.ID, HostID: 1, Port: 7003, Kind: "server", State: "stopped"})
	for _, k := range []string{KindHostOffline, KindMemory, KindReplBroken, KindInstanceDown} {
		if err := s.SaveRule(k, true, Defaults[k], []uint{ch.ID}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Evaluate(); err != nil {
		t.Fatal(err)
	}
	if len(h.reqs) != 3 {
		t.Fatalf("expected 3 notifications, got %d: %v", len(h.reqs), h.reqs)
	}
	// 再评估一次不重复通知
	if err := s.Evaluate(); err != nil || len(h.reqs) != 3 {
		t.Fatalf("duplicate notifications: %d %v", len(h.reqs), err)
	}
	// 主机恢复心跳 → 发恢复通知
	fresh := *now
	_, _ = q.Host.WithContext(ctx).Where(q.Host.Name.Eq("cache-1")).Update(q.Host.LastSeenAt, fresh)
	if err := s.Evaluate(); err != nil {
		t.Fatal(err)
	}
	if len(h.reqs) != 4 || h.reqs[3]["resolved"] != true {
		t.Fatalf("resolve notification missing: %v", h.reqs)
	}
	evs, _ := q.AlertEvent.WithContext(ctx).Find()
	firing := 0
	for _, e := range evs {
		if e.Status == "firing" {
			firing++
		}
		if !strings.Contains(e.Notify, "已发送") {
			t.Fatalf("notify result %q", e.Notify)
		}
	}
	if firing != 2 {
		t.Fatalf("firing = %d", firing)
	}
}

func TestDingTalkSignAndWeCom(t *testing.T) {
	s, now := newSvc(t, true)
	h := &hook{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	dt, err := s.SaveChannel(0, ChannelInput{Name: "dt", Type: "dingtalk", URL: srv.URL + "/robot/send?access_token=x", Secret: "SECabc", Enabled: true}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dt.SecretEnc, "SECabc") || dt.SecretEnc == "" {
		t.Fatal("secret must be stored encrypted")
	}
	if err := s.Send(dt, Message{Title: "t", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	ts := now.UnixMilli()
	mac := hmac.New(sha256.New, []byte("SECabc"))
	mac.Write([]byte(strings.TrimSpace(strings.Join([]string{itoa(ts), "SECabc"}, "\n"))))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !strings.Contains(h.urls[0], "timestamp="+itoa(ts)) || !strings.Contains(h.urls[0], "sign=") || !strings.Contains(unescape(h.urls[0]), want) {
		t.Fatalf("dingtalk url %s", h.urls[0])
	}
	wc, _ := s.SaveChannel(0, ChannelInput{Name: "wc", Type: "wecom", URL: srv.URL, Enabled: true}, "admin")
	if err := s.Send(wc, Message{Title: "t", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if h.reqs[1]["msgtype"] != "markdown" {
		t.Fatalf("wecom body %v", h.reqs[1])
	}
}

func TestIMRequiresLicense(t *testing.T) {
	s, _ := newSvc(t, false)
	if _, err := s.SaveChannel(0, ChannelInput{Name: "wc", Type: "wecom", URL: "https://qyapi.example.com/x", Enabled: true}, "admin"); !errors.Is(err, ErrIMNotLicensed) {
		t.Fatalf("wecom without alert_im: %v", err)
	}
	if _, err := s.SaveChannel(0, ChannelInput{Name: "h", Type: "webhook", URL: "https://hooks.example.com/x", Enabled: true}, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []ChannelInput{
		{Name: "e", Type: "email", URL: "smtp://mail.example.com", MailFrom: "a@b.c", MailTo: "x@y.z"},
		{Name: "e", Type: "email", URL: "smtp://mail.example.com:587", MailFrom: "a@b.c", MailTo: "x@y.z\r\nBcc: evil@x"},
		{Name: "w", Type: "webhook", URL: "file:///etc/passwd"},
		{Name: "w", Type: "webhook", URL: "https://user:pw@x.example.com/"},
	} {
		if _, err := s.SaveChannel(0, bad, "admin"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }

func unescape(u string) string {
	r := strings.NewReplacer("%2B", "+", "%2F", "/", "%3D", "=")
	return r.Replace(u)
}
