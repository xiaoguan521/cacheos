// Package api 是控制台的 REST API（CacheOS 规格书 7 节）。
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/alerts"
	"github.com/xiaoguan521/cacheos/internal/console/gateway"
	"github.com/xiaoguan521/cacheos/internal/console/ha"
	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/orch"
	"github.com/xiaoguan521/cacheos/internal/console/secpolicy"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
	"github.com/xiaoguan521/cacheos/internal/pkg/secretbox"
)

// Server 汇总 API 依赖。
type Server struct {
	Store         *store.Store
	GW            *gateway.Server
	Orch          *orch.Service
	Alerts        *alerts.Service
	Lic           *licsvc.Service
	Audit         *audit.Logger
	Box           *secretbox.Box // 加密 LDAP/OIDC 等配置中的凭据
	Log           *slog.Logger
	Web           fs.FS  // 前端构建产物（web/dist）
	GRPCAddr      string // agent 连接控制台使用的地址，写入安装命令
	CAFingerprint string
	SecureCookie  bool
	WebCAPEM      []byte      // -tls-auto 时的 Web CA 证书，公开下载以便导入浏览器
	HA            *ha.Elector // 主备选主；nil 视为单机
	// sendCmd 为指令下发通道，nil 时使用 GW.Send；测试替换它以检查发出的消息。
	sendCmd func(context.Context, uint, *agentv1.ServerMessage, time.Duration) (*agentv1.CommandResult, error)

	jwtKey        []byte
	fails         sync.Map // ip -> *loginFail
	migrationRuns sync.Map // migration id -> struct{}; prevents duplicate recovery in one process
	routes        func() gin.RoutesInfo
}

type loginFail struct {
	mu    sync.Mutex
	n     int
	until time.Time
}

const cookieName = "cacheos_session"

type auditE = audit.Entry

// Handler 构造 HTTP 路由。
func (s *Server) Handler() (http.Handler, error) {
	key, err := s.Store.GetKV("jwt_secret")
	if err != nil {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		key = hex.EncodeToString(b)
		if err := s.Store.SetKV("jwt_secret", key); err != nil {
			return nil, err
		}
	}
	s.jwtKey = []byte(key)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), s.securityHeaders)
	r.GET("/healthz", s.healthz)
	r.Use(s.standbyGuard)

	if len(s.WebCAPEM) > 0 {
		r.GET("/web-ca.crt", func(c *gin.Context) {
			c.Header("Content-Disposition", `attachment; filename="cacheos-console-web-ca.crt"`)
			c.Data(http.StatusOK, "application/x-x509-ca-cert", s.WebCAPEM)
		})
	}

	v1 := r.Group("/api/v1")
	v1.Use(s.csrf)
	v1.GET("/setup/status", s.setupStatus)
	v1.POST("/setup", s.setup)
	v1.POST("/auth/login", s.login)
	v1.POST("/auth/logout", s.logout)
	v1.GET("/auth/oidc/start", s.oidcStart)
	v1.GET("/auth/oidc/callback", s.oidcCallback)

	a := v1.Group("", s.auth)
	a.GET("/auth/me", s.me)
	a.GET("/auth/totp/status", s.totpStatus)
	a.POST("/auth/totp/setup", s.stepUp, s.setupTOTP)
	a.GET("/openapi.json", s.openAPI)

	// 告警（P9，规格书 7.1 M9）：企业微信 / 钉钉通道需要授权功能 alert_im
	a.GET("/alerts", s.alertsGet)
	a.POST("/alert-channels", role(store.RoleAdmin), s.writable, s.saveChannel)
	a.PUT("/alert-channels/:id", role(store.RoleAdmin), s.writable, s.saveChannel)
	a.DELETE("/alert-channels/:id", role(store.RoleAdmin), s.deleteChannel)
	a.POST("/alert-channels/:id/test", role(store.RoleAdmin), s.testChannel)
	a.PUT("/alert-rules/:kind", role(store.RoleAdmin), s.writable, s.saveRule)

	// 开放 API 令牌（P9）：创建需要授权功能 api_token；撤销任何时候都可以；令牌不能再创建令牌
	a.GET("/api-tokens", s.listTokens)
	a.POST("/api-tokens", s.writable, s.feature(license.FeatureAPIToken), s.createToken)
	a.DELETE("/api-tokens/:id", s.revokeToken)
	a.GET("/dashboard", s.dashboard)

	a.GET("/license", s.licenseGet)
	a.GET("/license/request", role(store.RoleAdmin), s.licenseRequest)
	a.POST("/license/import", role(store.RoleAdmin), s.licenseImport) // 过期状态下仍可用
	a.POST("/license/crl", role(store.RoleAdmin), s.licenseCRL)       // 过期状态下仍可用
	a.GET("/system/about", role(store.RoleAdmin), s.about)

	// 数据面 / agent 版本库（M14）：导入时验厂商签名，agent 只从已验签包拉取。
	a.GET("/engine-packages", role(store.RoleAdmin), s.listEnginePackages)
	a.POST("/engine-packages", role(store.RoleAdmin), s.writable, s.stepUp, s.importEnginePackage)
	a.DELETE("/engine-packages/:id", role(store.RoleAdmin), s.writable, s.stepUp, s.deleteEnginePackage)

	// 主机（规格书 7 节 M3）
	a.GET("/hosts", s.listHosts)
	a.GET("/hosts/:id", s.getHost)
	a.PUT("/hosts/:id", role(store.RoleOperator), s.updateHost)
	a.POST("/hosts/enroll", role(store.RoleOperator), s.writable, s.stepUp, s.enrollToken)
	a.POST("/hosts/:id/revoke", role(store.RoleAdmin), s.stepUp, s.revokeHost)
	a.POST("/hosts/:id/switch-version", role(store.RoleOperator), s.writable, s.stepUp, s.switchVersion)
	a.POST("/hosts/:id/upgrade-agent", role(store.RoleOperator), s.writable, s.stepUp, s.upgradeAgent)

	// 参数目录与模板（M5）
	a.GET("/params/catalog", s.paramCatalog)
	a.GET("/param-templates", s.listTemplates)
	a.POST("/param-templates", role(store.RoleOperator), s.writable, s.saveTemplate)
	a.PUT("/param-templates/:id", role(store.RoleOperator), s.writable, s.saveTemplate)
	a.DELETE("/param-templates/:id", role(store.RoleOperator), s.deleteTemplate)

	// 部署单元（M4、M6）：启停、切换与删除在授权只读时仍可用（授权失效不得影响已有实例，规格书 10 节）
	a.GET("/units", s.listUnits)
	a.POST("/units", role(store.RoleOperator), s.writable, s.stepUp, s.createUnit)
	a.GET("/units/:id", s.getUnit)
	a.DELETE("/units/:id", role(store.RoleOperator), s.stepUp, s.deleteUnit)
	a.POST("/units/:id/actions/:action", role(store.RoleOperator), s.stepUp, s.unitAction)
	a.POST("/units/:id/failover", role(store.RoleReleaser), s.stepUp, s.failover)
	a.POST("/units/:id/upgrade", role(store.RoleReleaser), s.writable, s.stepUp, s.upgradeUnit)
	a.POST("/units/:id/scale", role(store.RoleReleaser), s.writable, s.stepUp, s.scaleUnit)
	a.POST("/units/:id/sentinel", role(store.RoleReleaser), s.writable, s.stepUp, s.sentinelOperation)
	a.GET("/units/:id/revisions", role(store.RoleDeveloper), s.listRevisions)
	a.POST("/units/:id/params/preview", role(store.RoleDeveloper), s.previewParams)
	a.POST("/units/:id/params", role(store.RoleReleaser), s.writable, s.stepUp, s.applyParams)
	a.GET("/units/:id/metrics", s.unitMetrics)
	a.GET("/units/:id/diagnose", role(store.RoleDeveloper), s.diagnose)
	a.GET("/units/:id/keys", role(store.RoleDeveloper), s.browseKeys)
	a.GET("/units/:id/backups", role(store.RoleOperator), s.listBackups)
	a.GET("/units/:id/backup-policy", role(store.RoleOperator), s.getBackupPolicy)
	a.PUT("/units/:id/backup-policy", role(store.RoleOperator), s.writable, s.stepUp, s.saveBackupPolicy)
	a.POST("/units/:id/backups", role(store.RoleOperator), s.writable, s.stepUp, s.createBackup)
	a.POST("/units/:id/backups/:backup_id/restore", role(store.RoleOperator), s.writable, s.stepUp, s.restoreBackup)
	a.GET("/units/:id/migrations", role(store.RoleOperator), s.listMigrations)
	a.POST("/units/:id/migrations", role(store.RoleOperator), s.writable, s.stepUp, s.createMigration)
	a.POST("/units/:id/migrations/:migration_id/cutover", role(store.RoleOperator), s.writable, s.stepUp, s.cutoverMigration)
	a.POST("/units/:id/reshard", role(store.RoleReleaser), s.writable, s.stepUp, s.reshard)
	a.GET("/units/:id/accounts", role(store.RoleOperator), s.listAccounts)
	a.POST("/units/:id/accounts", role(store.RoleOperator), s.writable, s.stepUp, s.saveAccount)
	a.DELETE("/units/:id/accounts/:name", role(store.RoleOperator), s.writable, s.stepUp, s.deleteAccount)
	a.GET("/operations", s.listOperations)
	a.GET("/operations/:id", s.getOperation)
	a.POST("/operations/:id/approve", role(store.RoleReleaser), s.stepUp, s.approveOperation)
	a.POST("/operations/:id/resume", role(store.RoleReleaser), s.stepUp, s.resumeOperation)

	a.GET("/audit", role(store.RoleViewer), s.listAudit)
	a.GET("/audit/verify", role(store.RoleAdmin), s.verifyAudit)
	a.GET("/audit/export", role(store.RoleAdmin), s.feature(license.FeatureAuditExport), s.exportAudit)

	a.PUT("/auth/password", s.changePassword)
	a.GET("/sso/oidc", role(store.RoleAdmin), s.getOIDC)
	a.PUT("/sso/oidc", role(store.RoleAdmin), s.putOIDC)
	a.GET("/sso/ldap", role(store.RoleAdmin), s.getLDAP)
	a.PUT("/sso/ldap", role(store.RoleAdmin), s.putLDAP)
	a.POST("/sso/ldap/test", role(store.RoleAdmin), s.testLDAP)
	a.GET("/security-policy", role(store.RoleAdmin), s.getPolicy)
	a.PUT("/security-policy", role(store.RoleAdmin), s.putPolicy)
	a.GET("/approval-policy", role(store.RoleAdmin), s.getApprovalPolicy)
	a.PUT("/approval-policy", role(store.RoleAdmin), s.putApprovalPolicy)
	a.PUT("/users/:id", role(store.RoleAdmin), s.stepUp, s.updateUser)
	a.DELETE("/users/:id", role(store.RoleAdmin), s.stepUp, s.deleteUser)
	a.GET("/users", role(store.RoleAdmin), s.listUsers)
	a.POST("/users", role(store.RoleAdmin), s.writable, s.createUser)

	// 前端：静态文件 + SPA 回退到 index.html
	if s.Web != nil {
		files := http.FileServer(http.FS(s.Web))
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				fail(c, http.StatusNotFound, "NOT_FOUND", "not found")
				return
			}
			p := strings.TrimPrefix(c.Request.URL.Path, "/")
			if p == "" || !exists(s.Web, p) {
				c.Request.URL.Path = "/"
			}
			files.ServeHTTP(c.Writer, c.Request)
		})
	}
	s.routes = r.Routes
	return r, nil
}

func exists(f fs.FS, p string) bool {
	st, err := fs.Stat(f, p)
	return err == nil && !st.IsDir()
}

func (s *Server) securityHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; script-src 'self'")
	c.Next()
}

// csrf：修改类请求必须带自定义头（浏览器跨站表单无法设置），配合 SameSite=Strict Cookie。
func (s *Server) csrf(c *gin.Context) {
	// 令牌认证不依赖浏览器自动携带的凭据，不受 CSRF 影响
	if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
		c.Next()
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.GetHeader("X-Requested-With") != "cacheos" {
		fail(c, http.StatusForbidden, "CSRF", "missing X-Requested-With header")
		return
	}
	c.Next()
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, apiError{Code: code, Message: msg})
}

func failErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "NOT_FOUND", "not found")
	case errors.Is(err, store.ErrUnitBusy):
		fail(c, http.StatusConflict, "UNIT_BUSY", "该部署单元有进行中的变更，请等待其结束")
	case errors.Is(err, orch.ErrNoChanges):
		fail(c, http.StatusConflict, "NO_CHANGES", err.Error())
	case errors.Is(err, orch.ErrNoHosts):
		fail(c, http.StatusConflict, "NO_HOSTS", err.Error())
	case errors.Is(err, orch.ErrUnsupported):
		fail(c, http.StatusNotImplemented, "UNSUPPORTED", err.Error())
	case errors.Is(err, orch.ErrLicense):
		fail(c, http.StatusPaymentRequired, "LICENSE_LIMIT", err.Error())
	case errors.Is(err, gateway.ErrOffline):
		fail(c, http.StatusServiceUnavailable, "HOST_OFFLINE", err.Error())
	default:
		fail(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
}

// ---------- 认证与授权 ----------

type claims struct {
	UID    uint   `json:"uid"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	AuthAt int64  `json:"auth_at"` // 登录时间：会话最长有效期从这里算
	PC     bool   `json:"pc"`      // 必须先修改口令（首次登录或口令过期）
	jwt.RegisteredClaims
}

// issue 签发会话：空闲超时后失效，活动时滑动续期，自登录起最长 MaxHours（等保 2.0 三级）。
func (s *Server) issue(c *gin.Context, u *store.User) error {
	return s.issueAt(c, u, time.Now().Unix(), u.MustChangePassword || (isLocal(u) && s.policy().Expired(u.PasswordChangedAt, u.CreatedAt, time.Now())))
}

func (s *Server) issueAt(c *gin.Context, u *store.User, authAt int64, pc bool) error {
	pol := s.policy()
	idle := time.Duration(pol.IdleMinutes) * time.Minute
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{UID: u.ID, Name: u.Username, Role: u.Role, AuthAt: authAt, PC: pc,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(idle)), IssuedAt: jwt.NewNumericDate(time.Now())}})
	str, err := tok.SignedString(s.jwtKey)
	if err != nil {
		return err
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName, Value: str, Path: "/", HttpOnly: true, Secure: s.SecureCookie,
		SameSite: http.SameSiteStrictMode, MaxAge: pol.MaxHours * 3600})
	return nil
}

func (s *Server) auth(c *gin.Context) {
	if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
		s.tokenAuth(c)
		return
	}
	ck, err := c.Cookie(cookieName)
	if err != nil {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "login required")
		return
	}
	var cl claims
	tok, err := jwt.ParseWithClaims(ck, &cl, func(t *jwt.Token) (any, error) { return s.jwtKey, nil }, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !tok.Valid {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "session expired")
		return
	}
	pol := s.policy()
	if time.Since(time.Unix(cl.AuthAt, 0)) > time.Duration(pol.MaxHours)*time.Hour {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "session expired, please sign in again")
		return
	}
	// 每次请求按数据库中的账号状态判断：停用、删除、改角色立即生效
	uq := s.q().User
	u, err := uq.WithContext(c.Request.Context()).Where(uq.ID.Eq(cl.UID)).First()
	if err != nil || u.Disabled || u.Username != cl.Name {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "account disabled or removed")
		return
	}
	if cl.PC {
		p := strings.TrimPrefix(c.Request.URL.Path, "/api/v1")
		if p != "/auth/password" && p != "/auth/me" && p != "/auth/logout" {
			fail(c, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "请先修改口令")
			return
		}
	}
	// 滑动续期：剩余时间不足一半空闲超时时重新签发
	if cl.ExpiresAt != nil && time.Until(cl.ExpiresAt.Time) < time.Duration(pol.IdleMinutes)*time.Minute/2 {
		_ = s.issueAt(c, u, cl.AuthAt, cl.PC)
	}
	c.Set("uid", u.ID)
	c.Set("user", u.Username)
	c.Set("role", u.Role)
	c.Set("pc", cl.PC)
	c.Next()
}

func isLocal(u *store.User) bool { return u.Source == "" || u.Source == "local" }

// policy 返回当前账号安全策略。
func (s *Server) policy() secpolicy.Policy {
	v, _ := s.Store.GetKV("security_policy")
	return secpolicy.Parse(v)
}

// rank：viewer / auditor 只读；developer 可预检参数变更；releaser 可执行参数变更；operator 管理主机与部署单元；admin 管理账号与授权。
var rank = map[string]int{store.RoleViewer: 1, store.RoleAuditor: 1, store.RoleDeveloper: 2, store.RoleReleaser: 3, store.RoleOperator: 4, store.RoleAdmin: 5}

func role(min string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rank[c.GetString("role")] < rank[min] {
			fail(c, http.StatusForbidden, "FORBIDDEN", "requires role "+min)
			return
		}
		c.Next()
	}
}

// writable：授权过期 / 无效时拦截新增变更（规格书 10 节）。启停、切换、删除、导入 License 不挂此中间件。
func (s *Server) writable(c *gin.Context) {
	snap := s.Lic.Snapshot()
	if !snap.Writable() {
		fail(c, http.StatusLocked, "LICENSE_READ_ONLY", "授权状态为 "+string(snap.State)+"，控制台只读："+snap.Reason)
		return
	}
	c.Next()
}

func (s *Server) feature(f license.Feature) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.Lic.Snapshot().Feature(f) {
			fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", "当前授权不包含功能 "+string(f))
			return
		}
		c.Next()
	}
}

func (s *Server) audit(c *gin.Context, action, objType, objID string, detail any) {
	_ = s.Audit.Write(audit.Entry{Username: c.GetString("user"), IP: c.ClientIP(), Action: action, ObjectType: objType, ObjectID: objID, Detail: detail})
}
