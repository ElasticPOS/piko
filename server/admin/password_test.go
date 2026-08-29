package admin

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andydunstall/piko/pkg/auth"
	"github.com/andydunstall/piko/pkg/log"
)

// passwordServer starts an admin server with password authentication and
// returns its address.
func passwordServer(t *testing.T, password string, verifier *auth.MultiTenantVerifier) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := NewServer(
		nil,
		prometheus.NewRegistry(),
		verifier,
		password,
		nil,
		log.NewNopLogger(),
	)
	s.AddStatus("/fake", &fakeStatus{})
	go func() {
		require.NoError(t, s.Serve(ln))
	}()
	t.Cleanup(func() {
		//nolint
		s.Shutdown(context.TODO())
	})

	return ln.Addr().String()
}

func login(t *testing.T, addr string, password string) (*http.Response, []*http.Cookie) {
	t.Helper()

	body := fmt.Sprintf(`{"password":%q}`, password)
	resp, err := http.Post(
		fmt.Sprintf("http://%s/login", addr),
		"application/json",
		bytes.NewReader([]byte(body)),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		//nolint
		resp.Body.Close()
	})

	return resp, resp.Cookies()
}

func get(t *testing.T, addr string, path string, cookies []*http.Cookie) *http.Response {
	t.Helper()

	req, err := http.NewRequest(
		http.MethodGet, fmt.Sprintf("http://%s%s", addr, path), nil,
	)
	require.NoError(t, err)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		//nolint
		resp.Body.Close()
	})

	return resp
}

func TestServer_PasswordAuthentication(t *testing.T) {
	t.Run("login then read status", func(t *testing.T) {
		addr := passwordServer(t, "s3cret", nil)

		resp, cookies := login(t, addr, "s3cret")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.Len(t, cookies, 1)
		assert.Equal(t, sessionCookie, cookies[0].Name)
		assert.True(t, cookies[0].HttpOnly)
		// The session must never carry the password itself.
		assert.NotContains(t, cookies[0].Value, "s3cret")

		assert.Equal(t, http.StatusOK, get(t, addr, "/status/fake/foo", cookies).StatusCode)

		// Each login gets its own session ID.
		_, other := login(t, addr, "s3cret")
		require.Len(t, other, 1)
		assert.NotEqual(t, cookies[0].Value, other[0].Value)
		assert.Equal(t, http.StatusOK, get(t, addr, "/status/fake/foo", other).StatusCode)
	})

	t.Run("no session", func(t *testing.T) {
		addr := passwordServer(t, "s3cret", nil)

		assert.Equal(t, http.StatusUnauthorized, get(t, addr, "/status/fake/foo", nil).StatusCode)
		assert.Equal(t, http.StatusUnauthorized, get(t, addr, "/metrics", nil).StatusCode)
	})

	t.Run("incorrect password", func(t *testing.T) {
		addr := passwordServer(t, "s3cret", nil)

		resp, cookies := login(t, addr, "guess")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Empty(t, cookies)
	})

	t.Run("forged session", func(t *testing.T) {
		addr := passwordServer(t, "s3cret", nil)

		// A session signed with a different password, and one that has
		// expired, are both rejected.
		forged := newPasswordAuth("other", log.NewNopLogger()).
			sign("cafe", time.Now().Add(time.Hour).Unix())
		expired := newPasswordAuth("s3cret", log.NewNopLogger()).
			sign("cafe", time.Now().Add(-time.Minute).Unix())
		// A valid session with its ID swapped must fail the signature.
		tampered := "beef" + strings.TrimPrefix(
			newPasswordAuth("s3cret", log.NewNopLogger()).
				sign("cafe", time.Now().Add(time.Hour).Unix()), "cafe")

		for _, token := range []string{forged, expired, tampered, "not-a-token", ""} {
			cookies := []*http.Cookie{{Name: sessionCookie, Value: token}}
			assert.Equal(
				t, http.StatusUnauthorized,
				get(t, addr, "/status/fake/foo", cookies).StatusCode, token,
			)
		}
	})

	t.Run("logout", func(t *testing.T) {
		addr := passwordServer(t, "s3cret", nil)

		_, cookies := login(t, addr, "s3cret")
		require.Equal(t, http.StatusOK, get(t, addr, "/status/fake/foo", cookies).StatusCode)

		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/logout", addr), nil)
		require.NoError(t, err)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		require.Len(t, resp.Cookies(), 1)
		assert.Empty(t, resp.Cookies()[0].Value)
	})

	// The panel prompts for the password, so its shell must load without one.
	t.Run("panel shell bypasses password", func(t *testing.T) {
		addr := passwordServer(t, "s3cret", nil)

		assert.Equal(t, http.StatusOK, get(t, addr, "/", nil).StatusCode)
		assert.Equal(t, http.StatusOK, get(t, addr, "/health", nil).StatusCode)
	})

	// Without a password the panel must be able to tell that password login
	// isn't available, rather than being told its password was wrong.
	t.Run("login unavailable without a password", func(t *testing.T) {
		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(_ string) (*auth.Token, error) {
				return nil, auth.ErrInvalidToken
			},
		}, nil)
		addr := passwordServer(t, "", verifier)

		resp, cookies := login(t, addr, "anything")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Empty(t, cookies)
	})

	// Scripts and monitoring keep using tokens even when a password is set.
	t.Run("token still accepted", func(t *testing.T) {
		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				if token != "good" {
					return nil, auth.ErrInvalidToken
				}
				return &auth.Token{}, nil
			},
		}, nil)
		addr := passwordServer(t, "s3cret", verifier)

		for token, want := range map[string]int{
			"good": http.StatusOK,
			"bad":  http.StatusUnauthorized,
		} {
			req, err := http.NewRequest(
				http.MethodGet, fmt.Sprintf("http://%s/status/fake/foo", addr), nil,
			)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, want, resp.StatusCode, token)
		}

		// A session cookie still works alongside tokens.
		_, cookies := login(t, addr, "s3cret")
		assert.Equal(t, http.StatusOK, get(t, addr, "/status/fake/foo", cookies).StatusCode)
	})
}
