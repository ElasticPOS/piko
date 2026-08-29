package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/andydunstall/piko/pkg/log"
)

const (
	// sessionCookie holds the signed session issued after a successful login.
	sessionCookie = "piko_admin"

	// sessionTTL is how long a login lasts before the panel asks again.
	sessionTTL = 12 * time.Hour

	// failedLoginDelay slows down password guessing. The admin port is often
	// published publicly, and a password is far weaker than a signed token.
	failedLoginDelay = 500 * time.Millisecond
)

// passwordAuth authenticates the admin server with a single shared password,
// for operators who want to open the panel in a browser without minting
// tokens.
//
// A login issues a session cookie holding a random session ID and its expiry,
// signed with a key derived from the password. The password itself is neither
// stored in the cookie nor recoverable from it.
//
// Sessions are deliberately stateless. The admin port is served by every node
// and requests are load balanced across them, so a session ID looked up in
// one node's memory would be rejected the moment the next request landed
// elsewhere, and every deploy would sign everyone out. Signing instead lets
// any node verify a session another node issued, with no shared store.
//
// The trade-off is revocation: logging out clears the browser's cookie, but a
// copied cookie stays valid until it expires. Changing the password
// invalidates every session immediately, since the signing key is derived
// from it.
type passwordAuth struct {
	password string

	// key signs session IDs. Derived rather than using the password directly
	// so the signing key is separate from the credential being checked.
	key []byte

	logger log.Logger
}

func newPasswordAuth(password string, logger log.Logger) *passwordAuth {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("piko-admin-session-key/v1"))

	return &passwordAuth{
		password: password,
		key:      mac.Sum(nil),
		logger:   logger.WithSubsystem("admin.auth"),
	}
}

// newSession returns a session token for a new random session ID.
func (a *passwordAuth) newSession(expiry int64) (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	return a.sign(hex.EncodeToString(id), expiry), nil
}

// sign returns the session token for the given session ID and expiry.
func (a *passwordAuth) sign(id string, expiry int64) string {
	body := id + "." + strconv.FormatInt(expiry, 10)

	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(body))

	return body + "." + hex.EncodeToString(mac.Sum(nil))
}

// valid reports whether the token was issued by this server and hasn't
// expired.
func (a *passwordAuth) valid(token string) bool {
	id, rest, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	expiryStr, _, ok := strings.Cut(rest, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return false
	}
	return hmac.Equal([]byte(token), []byte(a.sign(id, expiry)))
}

// loginRoute exchanges the admin password for a session cookie.
func (a *passwordAuth) loginRoute(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(a.password)) != 1 {
		a.logger.Warn("failed admin login", zap.String("client-ip", c.ClientIP()))
		time.Sleep(failedLoginDelay)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "incorrect password"})
		return
	}

	expiry := time.Now().Add(sessionTTL)
	session, err := a.newSession(expiry.Unix())
	if err != nil {
		a.logger.Error("generate session", zap.Error(err))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	a.setCookie(c, session, int(sessionTTL.Seconds()))
	c.JSON(http.StatusOK, gin.H{"expires_at": expiry.Unix()})
}

// logoutRoute discards the session cookie.
func (a *passwordAuth) logoutRoute(c *gin.Context) {
	a.setCookie(c, "", -1)
	c.Status(http.StatusNoContent)
}

func (a *passwordAuth) setCookie(c *gin.Context, value string, maxAge int) {
	// Fly (and any other TLS terminating proxy) forwards plain HTTP, so the
	// forwarded scheme decides whether the cookie may be marked secure.
	secure := c.Request.TLS != nil ||
		strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// Verify is middleware that accepts requests carrying a valid session cookie.
//
// Requests without a session are rejected with the same response shape as
// token authentication, so the panel can treat both the same way.
func (a *passwordAuth) Verify(c *gin.Context) {
	cookie, err := c.Cookie(sessionCookie)
	if err != nil || !a.valid(cookie) {
		c.AbortWithStatusJSON(
			http.StatusUnauthorized,
			gin.H{"error": "missing authorization"},
		)
		return
	}
	c.Next()
}
