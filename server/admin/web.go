package admin

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

//go:embed web/index.html
var webFS embed.FS

// registerWeb registers the admin web panel, a self-contained HTML page that
// renders the status endpoints exposed by the admin server.
func (s *Server) registerWeb(router *gin.Engine) {
	index, err := webFS.ReadFile("web/index.html")
	if err != nil {
		// Embedded at build time, so this should never happen.
		s.logger.Error("read embedded web panel", zap.Error(err))
		return
	}

	serve := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	router.GET("/", serve)
	router.GET("/dashboard", serve)
}