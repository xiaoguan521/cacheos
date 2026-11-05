package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

func jsonRequest(t *testing.T, method, url string, body any) *http.Request {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, url, bytes.NewReader(b))
	r.RequestURI = ""
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "cacheos")
	return r
}

func TestConsoleSetupLoginAndCSRF(t *testing.T) {
	st := storetest.Open(t)
	lic := &licsvc.Service{Store: st, DataDir: t.TempDir(), Keys: license.BuiltinKeyRing()}
	if err := lic.Init(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Lic: lic, Audit: &audit.Logger{DB: st.DB}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	res, err := client.Get(ts.URL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup status: %d", res.StatusCode)
	}
	_ = res.Body.Close()

	res, err = client.Do(jsonRequest(t, http.MethodPost, ts.URL+"/api/v1/setup", map[string]string{
		"username": "admin",
		"password": "C0nsole!2026Safe",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		t.Fatalf("setup: %d %s", res.StatusCode, body)
	}
	_ = res.Body.Close()

	res, err = client.Get(ts.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"username":"admin"`) {
		t.Fatalf("me: %d %s", res.StatusCode, body)
	}

	res, err = client.Get(ts.URL + "/api/v1/license")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("license: %d", res.StatusCode)
	}
	_ = res.Body.Close()

	csrfReq := httptest.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
	csrfRes := httptest.NewRecorder()
	h.ServeHTTP(csrfRes, csrfReq)
	if csrfRes.Code != http.StatusForbidden {
		t.Fatalf("missing csrf header accepted: %d %s", csrfRes.Code, csrfRes.Body.String())
	}
}
