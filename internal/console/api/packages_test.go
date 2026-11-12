package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

func packageContext(method, path string, body io.Reader, contentType string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, body)
	if contentType != "" {
		c.Request.Header.Set("Content-Type", contentType)
	}
	return c, w
}

func TestImportEnginePackageRejectsInvalidSignature(t *testing.T) {
	st := storetest.Open(t)
	s := &Server{Store: st, Audit: &audit.Logger{DB: st.DB}}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	pkg, err := mw.CreateFormFile("package", "sycache-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pkg.Write([]byte("not a real package"))
	sig, err := mw.CreateFormFile("signature", "sycache-1.0.0.tar.gz.sig")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sig.Write([]byte(`{"format":"etos-pkg-sig-1"}`))
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	c, w := packageContext(http.MethodPost, "/api/v1/engine-packages", &body, mw.FormDataContentType())
	c.Set("user", "admin")
	s.importEnginePackage(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"SIGNATURE_INVALID"`) {
		t.Fatalf("invalid signature response: %d %s", w.Code, w.Body.String())
	}
}

func TestEnginePackageListHidesPayloadAndDeleteAudits(t *testing.T) {
	st := storetest.Open(t)
	s := &Server{Store: st, Audit: &audit.Logger{DB: st.DB}}
	p := &store.EnginePackage{
		Name: "sycache-agent", Version: "1.0.0", Arch: "amd64", SHA256: strings.Repeat("a", 64),
		Size: 4, KID: "etos-demo-pkg-2026", Sig: `{"format":"etos-pkg-sig-1","kid":"etos-demo-pkg-2026","name":"sycache-agent","version":"1.0.0","arch":"amd64","variant":"standard"}`,
		Data: []byte("test"), UploadedBy: "admin",
	}
	if err := st.DB.Create(p).Error; err != nil {
		t.Fatal(err)
	}

	c, w := packageContext(http.MethodGet, "/api/v1/engine-packages", nil, "")
	s.listEnginePackages(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0]["name"] != p.Name || listed[0]["variant"] != "standard" {
		t.Fatalf("unexpected list: %#v", listed)
	}
	if _, ok := listed[0]["data"]; ok {
		t.Fatal("package payload must not be returned by list")
	}

	c, w = packageContext(http.MethodDelete, "/api/v1/engine-packages/"+strconv.FormatUint(uint64(p.ID), 10), nil, "")
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(p.ID), 10)}}
	c.Set("user", "admin")
	s.deleteEnginePackage(c)
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("delete: %d %s", c.Writer.Status(), w.Body.String())
	}
	var count int64
	st.DB.Model(&store.EnginePackage{}).Count(&count)
	if count != 0 {
		t.Fatalf("package was not deleted: %d", count)
	}
	var auditCount int64
	st.DB.Model(&store.AuditLog{}).Where("action = ?", "engine_package.delete").Count(&auditCount)
	if auditCount != 1 {
		t.Fatalf("delete audit count: %d", auditCount)
	}
}
