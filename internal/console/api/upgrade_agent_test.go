package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/gateway"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

type sentCmd struct {
	hostID uint
	msg    *agentv1.ServerMessage
}

// upgradeHarness 准备一台在线 amd64 主机（agent 1.0.0）和版本库中的 agent 包。
type upgradeHarness struct {
	st   *store.Store
	s    *Server
	host store.Host
	sent []sentCmd
	res  *agentv1.CommandResult
	err  error
}

func newUpgradeHarness(t *testing.T) *upgradeHarness {
	t.Helper()
	st := storetest.Open(t)
	h := &upgradeHarness{st: st, res: &agentv1.CommandResult{Status: agentv1.CommandResult_OK}}
	h.s = &Server{Store: st, Audit: &audit.Logger{DB: st.DB}, GW: &gateway.Server{}}
	h.s.sendCmd = func(_ context.Context, hostID uint, m *agentv1.ServerMessage, _ time.Duration) (*agentv1.CommandResult, error) {
		h.sent = append(h.sent, sentCmd{hostID: hostID, msg: m})
		return h.res, h.err
	}
	h.host = store.Host{Name: "h1", Status: "online", AgentVersion: "1.0.0", Arch: "amd64", EngineCurrent: "1.30.6"}
	if err := st.DB.Create(&h.host).Error; err != nil {
		t.Fatal(err)
	}
	h.addPackage(t, "1.1.0", "amd64", "sycache-agent")
	return h
}

func (h *upgradeHarness) addPackage(t *testing.T, version, arch, name string) store.EnginePackage {
	t.Helper()
	p := store.EnginePackage{
		Name: name, Version: version, Arch: arch, SHA256: strings.Repeat("c", 64), Size: 4,
		KID: "cacheos-demo-pkg-2026", UploadedBy: "admin", Data: []byte("pkg!"),
		Sig: `{"format":"cacheos-pkg-sig-1","kid":"cacheos-demo-pkg-2026","name":"` + name + `","version":"` + version + `","arch":"` + arch + `","variant":"standard","sha256":"` + strings.Repeat("c", 64) + `","signature":"AAAA"}`,
	}
	if err := h.st.DB.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

func (h *upgradeHarness) post(t *testing.T, id uint, body any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	raw, _ := json.Marshal(body)
	c, w := packageContext(http.MethodPost, "/api/v1/hosts/"+strconv.FormatUint(uint64(id), 10)+"/upgrade-agent", bytes.NewReader(raw), "application/json")
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(id), 10)}}
	c.Set("user", "admin")
	c.Set("role", store.RoleOperator)
	h.s.upgradeAgent(c)
	return c, w
}

func TestUpgradeAgentSendsSignedUpgradeCommand(t *testing.T) {
	h := newUpgradeHarness(t)
	pkg := store.EnginePackage{}
	if err := h.st.DB.Where("name = ?", "sycache-agent").First(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	c, w := h.post(t, h.host.ID, map[string]string{"version": "1.1.0"})
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("upgrade: %d %s", c.Writer.Status(), w.Body.String())
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d commands, want 1", len(h.sent))
	}
	if h.sent[0].hostID != h.host.ID {
		t.Fatalf("sent to host %d, want %d", h.sent[0].hostID, h.host.ID)
	}
	up := h.sent[0].msg.GetUpgradeAgent()
	if up == nil {
		t.Fatalf("message body is %T, want UpgradeAgent", h.sent[0].msg.GetBody())
	}
	if up.Version != "1.1.0" || up.Arch != "amd64" || up.Sha256 != pkg.SHA256 || string(up.VendorSignature) != pkg.Sig {
		t.Fatalf("UpgradeAgent fields: %+v", up)
	}
	var n int64
	h.st.DB.Model(&store.AuditLog{}).Where("action = ?", "host.upgrade_agent").Count(&n)
	if n != 1 {
		t.Fatalf("upgrade audit count: %d", n)
	}
}

func TestUpgradeAgentRecordsFailureReason(t *testing.T) {
	h := newUpgradeHarness(t)
	h.res = &agentv1.CommandResult{Status: agentv1.CommandResult_FAILED, Message: "signature check failed"}
	_, w := h.post(t, h.host.ID, map[string]string{"version": "1.1.0"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"UPGRADE_FAILED"`) {
		t.Fatalf("failed upgrade response: %d %s", w.Code, w.Body.String())
	}
	var logs []store.AuditLog
	h.st.DB.Where("action = ?", "host.upgrade_agent").Find(&logs)
	if len(logs) != 1 || !strings.Contains(logs[0].Detail, `"result":"failed"`) || !strings.Contains(logs[0].Detail, "signature check failed") {
		t.Fatalf("failure not audited with reason: %+v", logs)
	}
}

func TestUpgradeAgentRejectsOfflineHost(t *testing.T) {
	h := newUpgradeHarness(t)
	h.st.DB.Model(&h.host).Update("status", "offline")
	_, w := h.post(t, h.host.ID, map[string]string{"version": "1.1.0"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"HOST_OFFLINE"`) {
		t.Fatalf("offline host response: %d %s", w.Code, w.Body.String())
	}
	if len(h.sent) != 0 {
		t.Fatal("command sent to offline host")
	}
}

func TestUpgradeAgentRejectsSessionGoneAfterCheck(t *testing.T) {
	h := newUpgradeHarness(t)
	h.err = gateway.ErrOffline
	_, w := h.post(t, h.host.ID, map[string]string{"version": "1.1.0"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"HOST_OFFLINE"`) {
		t.Fatalf("dropped session response: %d %s", w.Code, w.Body.String())
	}
}

func TestUpgradeAgentRejectsMissingVersion(t *testing.T) {
	h := newUpgradeHarness(t)
	_, w := h.post(t, h.host.ID, map[string]string{"version": "9.9.9"})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"AGENT_PACKAGE_NOT_FOUND"`) {
		t.Fatalf("missing package response: %d %s", w.Code, w.Body.String())
	}
	_, w = h.post(t, h.host.ID, map[string]string{"version": "1.1"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad version format: %d %s", w.Code, w.Body.String())
	}
	if len(h.sent) != 0 {
		t.Fatal("command sent for rejected request")
	}
}

func TestUpgradeAgentRejectsArchMismatch(t *testing.T) {
	h := newUpgradeHarness(t)
	h.addPackage(t, "1.2.0", "arm64", "sycache-agent")
	_, w := h.post(t, h.host.ID, map[string]string{"version": "1.2.0"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"ARCH_MISMATCH"`) {
		t.Fatalf("arch mismatch response: %d %s", w.Code, w.Body.String())
	}
	if len(h.sent) != 0 {
		t.Fatal("command sent for arch mismatch")
	}
}

func TestUpgradeAgentRejectsDowngradeAndSameVersion(t *testing.T) {
	h := newUpgradeHarness(t)
	for _, v := range []string{"1.0.0", "0.9.0"} {
		_, w := h.post(t, h.host.ID, map[string]string{"version": v})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"VERSION_NOT_NEWER"`) {
			t.Fatalf("version %s response: %d %s", v, w.Code, w.Body.String())
		}
	}
	if len(h.sent) != 0 {
		t.Fatal("command sent for downgrade or same version")
	}
}

func TestUpgradeAgentRoleChainRejectsViewer(t *testing.T) {
	h := newUpgradeHarness(t)
	// 与路由登记的中间件链一致：role(operator) 在最前。
	r := gin.New()
	r.POST("/api/v1/hosts/:id/upgrade-agent", role(store.RoleOperator), h.s.upgradeAgent)
	raw, _ := json.Marshal(map[string]string{"version": "1.1.0"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/"+strconv.FormatUint(uint64(h.host.ID), 10)+"/upgrade-agent", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	// 中间件从上下文读取角色；这里直接模拟只读角色。
	r.Use(func(c *gin.Context) { c.Set("role", store.RoleViewer); c.Next() })
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || len(h.sent) != 0 {
		t.Fatalf("viewer upgrade: %d %s, sent=%d", w.Code, w.Body.String(), len(h.sent))
	}
}

func TestImportAgentPackageRejectsBadSignature(t *testing.T) {
	st := storetest.Open(t)
	s := &Server{Store: st, Audit: &audit.Logger{DB: st.DB}}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("agent tarball")
	// 用未受信任的密钥签名：导入必须拒绝（不存在"跳过验签"开关）。
	sig, err := pkgsig.Sign(priv, "cacheos-pkg-0001", "sycache-agent", "1.1.0", "amd64", "standard", data)
	if err != nil {
		t.Fatal(err)
	}
	sigJSON, _ := json.Marshal(sig)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	pkgPart, _ := mw.CreateFormFile("package", "sycache-agent-1.1.0.tar.gz")
	_, _ = pkgPart.Write(data)
	sigPart, _ := mw.CreateFormFile("signature", "sycache-agent-1.1.0.tar.gz.sig")
	_, _ = sigPart.Write(sigJSON)
	_ = mw.Close()

	c, w := packageContext(http.MethodPost, "/api/v1/engine-packages", &body, mw.FormDataContentType())
	c.Set("user", "admin")
	s.importEnginePackage(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"SIGNATURE_INVALID"`) {
		t.Fatalf("untrusted agent package: %d %s", w.Code, w.Body.String())
	}
	var n int64
	st.DB.Model(&store.EnginePackage{}).Count(&n)
	if n != 0 {
		t.Fatalf("rejected package was stored: %d", n)
	}
}
