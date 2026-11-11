package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xiaoguan521/cacheos/internal/version"
)

func (s *Server) about(c *gin.Context) {
	current := version.Version
	if len(version.Releases) > 0 {
		current = version.Releases[0].Product
	}
	c.JSON(http.StatusOK, gin.H{
		"product":          version.Product,
		"component":        "console",
		"version":          version.Version,
		"commit":           version.Commit,
		"build_date":       version.BuildDate,
		"based_on":         "Valkey (BSD-3-Clause)",
		"notice":           "docs/NOTICE; installed data-plane packages ship /home/atwasoft/sycache/licenses/NOTICE",
		"current_release":  current,
		"release_mappings": version.Releases,
	})
}
