package admin

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andydunstall/piko/pkg/auth"
	"github.com/andydunstall/piko/pkg/log"
	"github.com/andydunstall/piko/pkg/testutil"
	"github.com/andydunstall/piko/server/cluster"
	"github.com/andydunstall/piko/server/status"
)

type fakeStatus struct {
}

func (s *fakeStatus) Register(group *gin.RouterGroup) {
	group.GET("/foo", s.fooRoute)
}

func (s *fakeStatus) fooRoute(c *gin.Context) {
	c.String(http.StatusOK, "foo")
}

var _ status.Handler = &fakeStatus{}

type fakeVerifier struct {
	handler func(token string) (*auth.Token, error)
}

func (v *fakeVerifier) Verify(token string) (*auth.Token, error) {
	return v.handler(token)
}

var _ auth.Verifier = &fakeVerifier{}

func TestServer_AdminRoutes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := NewServer(
		nil,
		prometheus.NewRegistry(),
		nil,
		nil,
		log.NewNopLogger(),
	)
	go func() {
		require.NoError(t, s.Serve(ln))
	}()
	defer s.Shutdown(context.TODO())

	t.Run("health", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/health", ln.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("ready", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/ready", ln.Addr().String())

		// Not ready.

		s.SetReady(false)

		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

		// Ready.

		s.SetReady(true)

		resp, err = http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("metrics", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/metrics", ln.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("not found", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/foo", ln.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestServer_StatusRoutes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := NewServer(
		nil,
		prometheus.NewRegistry(),
		nil,
		nil,
		log.NewNopLogger(),
	)
	s.AddStatus("/mystatus", &fakeStatus{})

	go func() {
		require.NoError(t, s.Serve(ln))
	}()
	defer s.Shutdown(context.TODO())

	t.Run("status ok", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/status/mystatus/foo", ln.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		buf := new(bytes.Buffer)
		//nolint
		buf.ReadFrom(resp.Body)
		assert.Equal(t, []byte("foo"), buf.Bytes())
	})

	t.Run("not found", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/status/notfound", ln.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

// TestServer_Forward tests forwarding an admin request to another node
// in the cluster.
func TestServer_Forward(t *testing.T) {
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	state1 := cluster.NewState(&cluster.Node{
		ID:        "node-1",
		AdminAddr: ln1.Addr().String(),
	}, log.NewNopLogger())

	s1 := NewServer(
		state1,
		prometheus.NewRegistry(),
		nil,
		nil,
		log.NewNopLogger(),
	)
	// Note only node 1 registers the status route.
	s1.AddStatus("/mystatus", &fakeStatus{})

	go func() {
		require.NoError(t, s1.Serve(ln1))
	}()
	defer s1.Shutdown(context.TODO())

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	state2 := cluster.NewState(&cluster.Node{
		ID:        "node-2",
		AdminAddr: ln2.Addr().String(),
	}, log.NewNopLogger())
	state2.AddNode(&cluster.Node{
		ID:        "node-1",
		AdminAddr: ln1.Addr().String(),
	})

	s2 := NewServer(
		state2,
		prometheus.NewRegistry(),
		nil,
		nil,
		log.NewNopLogger(),
	)

	go func() {
		require.NoError(t, s2.Serve(ln2))
	}()
	defer s2.Shutdown(context.TODO())

	t.Run("forward ok", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/status/mystatus/foo?forward=node-1", ln2.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		buf := new(bytes.Buffer)
		//nolint
		buf.ReadFrom(resp.Body)
		assert.Equal(t, []byte("foo"), buf.Bytes())
	})

	t.Run("forward not found", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/status/mystatus/foo?forward=node-3", ln2.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestServer_Authentication(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return &auth.Token{
					Expiry: time.Now().Add(time.Hour),
				}, nil
			},
		}, nil)

		s := NewServer(
			nil,
			prometheus.NewRegistry(),
			verifier,
			nil,
			log.NewNopLogger(),
		)
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf("http://%s/metrics", ln.Addr().String())
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Add("Authorization", "Bearer 123")

		client := &http.Client{}
		resp, err := client.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("unauthenticated", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return nil, auth.ErrInvalidToken
			},
		}, nil)

		s := NewServer(
			nil,
			prometheus.NewRegistry(),
			verifier,
			nil,
			log.NewNopLogger(),
		)
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf("http://%s/metrics", ln.Addr().String())
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Add("Authorization", "Bearer 123")

		client := &http.Client{}
		resp, err := client.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	// Health checks can't send a token, so the probes must stay reachable
	// while everything else on the admin server is gated.
	t.Run("probes bypass auth", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(_ string) (*auth.Token, error) {
				assert.Fail(t, "probes must not be authenticated")
				return nil, auth.ErrInvalidToken
			},
		}, nil)

		s := NewServer(
			nil,
			prometheus.NewRegistry(),
			verifier,
			nil,
			log.NewNopLogger(),
		)
		s.SetReady(true)
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		client := &http.Client{}
		for _, path := range []string{"/health", "/ready"} {
			url := fmt.Sprintf("http://%s%s", ln.Addr().String(), path)
			// No Authorization header.
			req, _ := http.NewRequest(http.MethodGet, url, nil)

			resp, err := client.Do(req)
			assert.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		}
	})

	t.Run("data routes are gated", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(_ string) (*auth.Token, error) {
				return nil, auth.ErrInvalidToken
			},
		}, nil)

		s := NewServer(
			nil,
			prometheus.NewRegistry(),
			verifier,
			nil,
			log.NewNopLogger(),
		)
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		client := &http.Client{}
		for _, path := range []string{"/debug/pprof/", "/metrics"} {
			url := fmt.Sprintf("http://%s%s", ln.Addr().String(), path)
			req, _ := http.NewRequest(http.MethodGet, url, nil)

			resp, err := client.Do(req)
			assert.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, path)
		}
	})

	// A browser can't attach an 'Authorization' header to a top-level
	// navigation, so the panel shell must load unauthenticated. It contains no
	// cluster data: it prompts for a credential and attaches it to the API
	// calls it makes, which are gated by the test above.
	t.Run("panel shell bypasses auth", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(_ string) (*auth.Token, error) {
				assert.Fail(t, "the panel shell must not be authenticated")
				return nil, auth.ErrInvalidToken
			},
		}, nil)

		s := NewServer(
			nil,
			prometheus.NewRegistry(),
			verifier,
			nil,
			log.NewNopLogger(),
		)
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		client := &http.Client{}
		for _, path := range []string{"/", "/dashboard"} {
			url := fmt.Sprintf("http://%s%s", ln.Addr().String(), path)
			req, _ := http.NewRequest(http.MethodGet, url, nil)

			resp, err := client.Do(req)
			assert.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode, path)
			assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"), path)

			body, err := io.ReadAll(resp.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), "Piko Admin", path)
		}
	})
}

func TestServer_TLS(t *testing.T) {
	rootCAPool, cert, err := testutil.LocalTLSServerCert()
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	tlsConfig := &tls.Config{}
	tlsConfig.Certificates = []tls.Certificate{cert}

	s := NewServer(
		nil,
		prometheus.NewRegistry(),
		nil,
		tlsConfig,
		log.NewNopLogger(),
	)
	go func() {
		require.NoError(t, s.Serve(ln))
	}()
	defer s.Shutdown(context.TODO())

	t.Run("https ok", func(t *testing.T) {
		tlsConfig = &tls.Config{
			RootCAs: rootCAPool,
		}
		transport := &http.Transport{
			TLSClientConfig: tlsConfig,
		}
		client := &http.Client{
			Transport: transport,
		}

		req, _ := http.NewRequest(
			http.MethodGet,
			fmt.Sprintf("https://%s/health", ln.Addr().String()),
			nil,
		)
		resp, err := client.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("https bad ca", func(t *testing.T) {
		url := fmt.Sprintf("https://%s/health", ln.Addr().String())
		_, err := http.Get(url)
		assert.ErrorContains(t, err, "verify certificate")
	})

	t.Run("http", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/health", ln.Addr().String())
		resp, err := http.Get(url)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}
