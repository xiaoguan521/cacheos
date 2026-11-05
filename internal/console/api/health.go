package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xiaoguan521/cacheos/internal/version"
)

func (s *Server) leader() bool { return s.HA == nil || s.HA.IsLeader() }

// healthz 供 keepalived / 负载均衡探测（无需登录，不含敏感信息）：
// 主控制台且数据库可用时返回 200；备控制台或数据库不可用时返回 503。
func (s *Server) healthz(c *gin.Context) {
	out := gin.H{"version": version.Version, "role": "leader", "ha": s.HA != nil && s.HA.Enabled()}
	code := http.StatusOK
	if !s.leader() {
		out["role"] = "standby"
		code = http.StatusServiceUnavailable
	} else if s.HA != nil && !s.HA.Since().IsZero() {
		out["leader_since"] = s.HA.Since().UTC().Format(time.RFC3339)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if db, err := s.Store.DB.DB(); err != nil || db.PingContext(ctx) != nil {
		out["db"] = "unavailable"
		code = http.StatusServiceUnavailable
	} else {
		out["db"] = "ok"
	}
	out["status"] = map[bool]string{true: "ok", false: "unavailable"}[code == http.StatusOK]
	c.Header("Cache-Control", "no-store")
	c.JSON(code, out)
}

// standbyGuard：备控制台不提供 Web 控制台与 API（后台任务与节点连接都在主控制台），避免在备机上做出不会下发的修改。
func (s *Server) standbyGuard(c *gin.Context) {
	if s.leader() {
		c.Next()
		return
	}
	c.Header("Retry-After", "5")
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": "STANDBY", "message": "这是备用控制台，请通过 VIP 访问主控制台"})
}
