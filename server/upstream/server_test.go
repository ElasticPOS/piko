package upstream

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andydunstall/piko/pkg/auth"
	"github.com/andydunstall/piko/pkg/log"
	"github.com/andydunstall/piko/pkg/testutil"
	"github.com/andydunstall/piko/pkg/websocket"
	"github.com/andydunstall/piko/server/config"
)

type fakeManager struct {
	addConnCh    chan Upstream
	removeConnCh chan Upstream
}

func newFakeManager() *fakeManager {
	return &fakeManager{
		addConnCh:    make(chan Upstream),
		removeConnCh: make(chan Upstream),
	}
}

func (m *fakeManager) Select(_ string, _ bool) (Upstream, bool) {
	return nil, false
}

func (m *fakeManager) AddConn(u Upstream) {
	m.addConnCh <- u
}

func (m *fakeManager) RemoveConn(u Upstream) {
	m.removeConnCh <- u
}

type fakeVerifier struct {
	handler func(token string) (*auth.Token, error)
}

func (v *fakeVerifier) Verify(token string) (*auth.Token, error) {
	return v.handler(token)
}

var _ auth.Verifier = &fakeVerifier{}

func TestServer_Register(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		s := NewServer(manager, nil, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		conn, err := websocket.Dial(context.TODO(), url)
		require.NoError(t, err)

		addedUpstream := <-manager.addConnCh
		assert.Equal(t, "my-endpoint", addedUpstream.EndpointID())

		conn.Close()

		removedUpstream := <-manager.removeConnCh
		assert.Equal(t, "my-endpoint", removedUpstream.EndpointID())
	})

	// Tests the client supplied name is recorded against the upstream, after
	// being sanitized.
	t.Run("name", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		s := NewServer(manager, nil, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint?name=%s",
			ln.Addr().String(),
			neturl.QueryEscape("  my-host\n  "),
		)
		conn, err := websocket.Dial(context.TODO(), url)
		require.NoError(t, err)
		defer conn.Close()

		addedUpstream := <-manager.addConnCh
		metadata := addedUpstream.(*ConnUpstream).Metadata()
		assert.Equal(t, "my-host", metadata.Name)
		assert.Equal(t, "127.0.0.1", metadata.Addr)
		assert.NotZero(t, metadata.ConnectedAt)
	})

	// Tests the server closes upstream connections when it is shutdown.
	t.Run("close on shutdown", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		s := NewServer(manager, nil, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		conn, err := websocket.Dial(context.TODO(), url)
		require.NoError(t, err)
		defer conn.Close()

		addedUpstream := <-manager.addConnCh
		assert.Equal(t, "my-endpoint", addedUpstream.EndpointID())

		// Shutdown the server which should close the upstream connection.
		s.Shutdown(context.TODO())

		removedUpstream := <-manager.removeConnCh
		assert.Equal(t, "my-endpoint", removedUpstream.EndpointID())
	})
}

func TestServer_Authentication(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return &auth.Token{
					Expiry:    time.Now().Add(time.Hour),
					Endpoints: []string{"my-endpoint"},
				}, nil
			},
		}, nil)

		s := NewServer(manager, verifier, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		conn, err := websocket.Dial(context.TODO(), url, websocket.WithToken("123"))
		require.NoError(t, err)

		addedUpstream := <-manager.addConnCh
		assert.Equal(t, "my-endpoint", addedUpstream.EndpointID())

		conn.Close()

		removedUpstream := <-manager.removeConnCh
		assert.Equal(t, "my-endpoint", removedUpstream.EndpointID())
	})

	t.Run("token expires", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return &auth.Token{
					// Set a short expiry as we wait for the token to expire.
					Expiry:    time.Now().Add(time.Millisecond * 10),
					Endpoints: []string{"my-endpoint"},
				}, nil
			},
		}, nil)

		s := NewServer(manager, verifier, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		conn, err := websocket.Dial(context.TODO(), url, websocket.WithToken("123"))
		require.NoError(t, err)
		defer conn.Close()

		addedUpstream := <-manager.addConnCh
		assert.Equal(t, "my-endpoint", addedUpstream.EndpointID())

		// Token should expire without closing client.

		removedUpstream := <-manager.removeConnCh
		assert.Equal(t, "my-endpoint", removedUpstream.EndpointID())
	})

	t.Run("endpoint not permitted", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return &auth.Token{
					Expiry:    time.Now().Add(time.Hour),
					Endpoints: []string{"foo"},
				}, nil
			},
		}, nil)

		s := NewServer(manager, verifier, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		_, err = websocket.Dial(context.TODO(), url, websocket.WithToken("123"))
		require.ErrorContains(t, err, "401: endpoint not permitted")
	})

	// Tests authenticating with a token that doesn't contain any endpoints
	// (meaning the client can access ALL endpoints).
	t.Run("token missing endpoints", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return &auth.Token{
					Expiry: time.Now().Add(time.Hour),
				}, nil
			},
		}, nil)

		s := NewServer(manager, verifier, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		conn, err := websocket.Dial(context.TODO(), url, websocket.WithToken("123"))
		require.NoError(t, err)

		addedUpstream := <-manager.addConnCh
		assert.Equal(t, "my-endpoint", addedUpstream.EndpointID())

		conn.Close()

		removedUpstream := <-manager.removeConnCh
		assert.Equal(t, "my-endpoint", removedUpstream.EndpointID())
	})

	t.Run("unauthenticated", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		manager := newFakeManager()

		verifier := auth.NewMultiTenantVerifier(&fakeVerifier{
			handler: func(token string) (*auth.Token, error) {
				assert.Equal(t, "123", token)
				return nil, auth.ErrInvalidToken
			},
		}, nil)

		s := NewServer(manager, verifier, nil, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
		go func() {
			require.NoError(t, s.Serve(ln))
		}()
		defer s.Shutdown(context.TODO())

		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		_, err = websocket.Dial(context.TODO(), url, websocket.WithToken("123"))
		require.ErrorContains(t, err, "401: invalid token")
	})
}

func TestServer_TLS(t *testing.T) {
	rootCAPool, cert, err := testutil.LocalTLSServerCert()
	require.NoError(t, err)

	tlsConfig := &tls.Config{}
	tlsConfig.Certificates = []tls.Certificate{cert}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	manager := newFakeManager()

	s := NewServer(manager, nil, tlsConfig, nil, config.UpstreamConfig{}, config.StreamConfig{MaxWindowSize: 256 * 1024}, log.NewNopLogger())
	go func() {
		require.NoError(t, s.Serve(ln))
	}()
	defer s.Shutdown(context.TODO())

	t.Run("wss ok", func(t *testing.T) {
		url := fmt.Sprintf(
			"wss://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		clientTLSConfig := &tls.Config{
			RootCAs: rootCAPool,
		}
		conn, err := websocket.Dial(
			context.TODO(), url, websocket.WithTLSConfig(clientTLSConfig),
		)
		require.NoError(t, err)

		addedUpstream := <-manager.addConnCh
		assert.Equal(t, "my-endpoint", addedUpstream.EndpointID())

		conn.Close()

		removedUpstream := <-manager.removeConnCh
		assert.Equal(t, "my-endpoint", removedUpstream.EndpointID())
	})

	t.Run("wss bad ca", func(t *testing.T) {
		url := fmt.Sprintf(
			"wss://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		_, err := websocket.Dial(context.TODO(), url)
		require.ErrorContains(t, err, "verify certificate")
	})

	t.Run("ws", func(t *testing.T) {
		url := fmt.Sprintf(
			"ws://%s/piko/v1/upstream/my-endpoint",
			ln.Addr().String(),
		)
		_, err := websocket.Dial(context.TODO(), url)
		require.ErrorContains(t, err, "bad handshake")
	})
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "", want: ""},
		{name: "my-host", want: "my-host"},
		{name: "  my-host  ", want: "my-host"},
		// Control characters are stripped as the name is written to the logs
		// and the status API.
		{name: "my\rhost\x00", want: "myhost"},
		// Long names are truncated by rune, not byte, so the result stays
		// valid UTF-8.
		{name: strings.Repeat("\u00e9", maxNameLen+10), want: strings.Repeat("\u00e9", maxNameLen)},
	}
	for _, test := range tests {
		assert.Equal(t, test.want, sanitizeName(test.name))
	}
}
