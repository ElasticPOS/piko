package admin

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

//go:embed web/index.html
var webFS embed.FS

// Content security policy for the panel. Everything it needs is inline and
// same-origin, so nothing else is allowed to load or be contacted.
const webCSP = "default-src 'none'; " +
	"script-src 'unsafe-inline'; " +
	"style-src 'unsafe-inline'; " +
	"img-src data:; " +
	"connect-src 'self'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// registerWeb registers the admin web panel, a self-contained HTML page that
// renders the status endpoints exposed by the admin server.
//
// The page itself carries no cluster data, so it is registered before the
// authentication middleware: a browser can't attach an 'Authorization' header
// to a top-level navigation, so gating the shell would leave the panel
// unreachable in a browser. The panel prompts for a credential and sends it
// with every API call it makes, which stay gated.
func (s *Server) registerWeb(router *gin.Engine) {
	index, err := webFS.ReadFile("web/index.html")
	if err != nil {
		// Embedded at build time, so this should never happen.
		s.logger.Error("read embedded web panel", zap.Error(err))
		return
	}

	serve := func(c *gin.Context) {
		c.Header("Content-Security-Policy", webCSP)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	router.GET("/", serve)
	router.GET("/dashboard", serve)
}
