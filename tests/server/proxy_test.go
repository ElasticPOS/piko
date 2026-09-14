//go:build system

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"

	"github.com/andydunstall/piko/client"
	"github.com/andydunstall/piko/pikotest/cluster"
	"github.com/andydunstall/piko/pkg/auth"
)

type errorMessage struct {
	Error string `json:"error"`
}

// Tests proxying HTTP traffic with a single Piko server node.
func TestProxy_HTTP(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		node := cluster.NewNode()
		node.Start()
		defer node.Stop()

		// Add upstream listener with a HTTP server returning 200.

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		server := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				// Note can't use io.Copy as not supported by http.ResponseWriter.
				b, err := io.ReadAll(r.Body)
				if err != nil {
					panic(fmt.Sprintf("read body: %s", err.Error()))
				}
				n, err := w.Write(b)
				if err != nil {
					panic(fmt.Sprintf("write bytes: %d: %s", n, err))
				}
			},
		))
		server.Listener = ln
		go server.Start()
		defer server.Close()

		// Send a request to the upstream via Piko.

		reqBody := randomBytes(4 * 1024)
		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+node.ProxyAddr(),
			bytes.NewReader(reqBody),
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		respBody, err := io.ReadAll(resp.Body)
		assert.NoError(t, err)
		assert.Equal(t, reqBody, respBody)
	})

	t.Run("https", func(t *testing.T) {
		node := cluster.NewNode(cluster.WithTLS(true))
		node.Start()
		defer node.Stop()

		clientTLSConfig := &tls.Config{
			RootCAs: node.RootCAPool(),
		}

		// Add upstream listener with a HTTP server returning 200.

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "https",
				Host:   node.UpstreamAddr(),
			},
			TLSConfig: clientTLSConfig,
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		server := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				// Note can't use io.Copy as not supported by http.ResponseWriter.
				b, err := io.ReadAll(r.Body)
				if err != nil {
					panic(fmt.Sprintf("read body: %s", err.Error()))
				}
				n, err := w.Write(b)
				if err != nil {
					panic(fmt.Sprintf("write bytes: %d: %s", n, err))
				}
			},
		))
		server.Listener = ln
		go server.Start()
		defer server.Close()

		// Send a request to the upstream via Piko.

		reqBody := randomBytes(4 * 1024)
		req, _ := http.NewRequest(
			http.MethodGet,
			"https://"+node.ProxyAddr(),
			bytes.NewReader(reqBody),
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTLSConfig,
			},
		}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		respBody, err := io.ReadAll(resp.Body)
		assert.NoError(t, err)
		assert.Equal(t, reqBody, respBody)
	})

	t.Run("websocket", func(t *testing.T) {
		node := cluster.NewNode()
		node.Start()
		defer node.Stop()

		// Add upstream listener with a WebSocket server that echos back the
		// first message.

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		server := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				var upgrader = websocket.Upgrader{}

				c, err := upgrader.Upgrade(w, r, nil)
				assert.NoError(t, err)
				defer c.Close()
				mt, message, err := c.ReadMessage()
				assert.NoError(t, err)

				assert.NoError(t, c.WriteMessage(mt, message))
			},
		))
		server.Listener = ln
		go server.Start()
		defer server.Close()

		// Send a WebSocket message via Piko and wait for it to be echoed back.

		header := make(http.Header)
		header.Add("x-piko-endpoint", "my-endpoint")

		c, _, err := websocket.DefaultDialer.Dial("ws://"+node.ProxyAddr(), header)
		assert.NoError(t, err)
		defer c.Close()

		assert.NoError(t, c.WriteMessage(websocket.TextMessage, []byte("echo")))

		mt, message, err := c.ReadMessage()
		assert.NoError(t, err)

		assert.Equal(t, websocket.TextMessage, mt)
		assert.Equal(t, []byte("echo"), message)
	})

	// Tests sending a request to an endpoint with no listeners.
	t.Run("no listeners", func(t *testing.T) {
		node := cluster.NewNode()
		node.Start()
		defer node.Stop()

		// Send a request to endpoint 'my-endpoint' with no upstream listeners.

		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+node.ProxyAddr(),
			nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
		m := errorMessage{}
		assert.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
		assert.Equal(t, "no available upstreams", m.Error)
	})
}

// Tests proxying HTTP traffic to a group of upstreams within an endpoint.
func TestProxy_Group(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	// Add an upstream listener for each branch, both listening on the same
	// endpoint, whose HTTP server echoes the branch it belongs to and the path
	// it was asked for.

	listen := func(group string) {
		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
			Group: group,
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		server := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if _, err := w.Write([]byte(group + " " + r.URL.Path)); err != nil {
					panic("write bytes: " + err.Error())
				}
			},
		))
		server.Listener = ln
		go server.Start()
		t.Cleanup(server.Close)
	}
	listen("branch-1")
	listen("branch-2")

	get := func(path string) (int, string) {
		req, _ := http.NewRequest(
			http.MethodGet, "http://"+node.ProxyAddr()+path, nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")

		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		assert.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	// A request addressed to a group is served by that group alone, with the
	// group prefix stripped.

	status, body := get("/_g/branch-1/foo")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "branch-1 /foo", body)

	status, body = get("/_g/branch-2/foo")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "branch-2 /foo", body)

	// A request addressed to the endpoint is served by either group, without
	// the upstream seeing a group at all.

	status, body = get("/foo")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, []string{"branch-1 /foo", "branch-2 /foo"}, body)

	// A group nobody has joined has no upstreams, rather than falling back to
	// the endpoint.

	status, body = get("/_g/branch-3/foo")
	assert.Equal(t, http.StatusBadGateway, status)
	m := errorMessage{}
	assert.NoError(t, json.NewDecoder(strings.NewReader(body)).Decode(&m))
	assert.Equal(t, "no available upstreams", m.Error)
}

func TestProxy_TCP(t *testing.T) {
	t.Run("tcp", func(t *testing.T) {
		node := cluster.NewNode()
		node.Start()
		defer node.Stop()

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			for {
				defer wg.Done()
				conn, err := ln.Accept()
				if errors.Is(err, client.ErrClosed) {
					return
				}
				assert.NoError(t, err)

				// Echo server.
				buf := make([]byte, 512)
				for {
					n, err := conn.Read(buf)
					if err == io.EOF {
						return
					}
					assert.NoError(t, err)
					_, err = conn.Write(buf[:n])
					assert.NoError(t, err)
				}
			}
		}()

		dialer := client.Dialer{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.ProxyAddr(),
			},
		}
		conn, err := dialer.Dial(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		// Test writing bytes to the upstream and waiting for them to be
		// echoed back.

		buf := make([]byte, 512)
		for i := 0; i != 1; i++ {
			_, err = conn.Write([]byte("foo"))
			assert.NoError(t, err)

			n, err := conn.Read(buf)
			assert.NoError(t, err)
			assert.Equal(t, 3, n)
		}

		// Verify closing the connection to Piko also closes the connection
		// to the upstream.
		conn.Close()
		wg.Wait()
	})
}

// Tests proxying TCP traffic to a group of upstreams within an endpoint.
func TestProxy_TCPGroup(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	// Add a listener for each branch, both listening on the same endpoint,
	// which reply with the branch they belong to.

	listen := func(group string) {
		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
			Group: group,
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)
		t.Cleanup(func() {
			ln.Close()
		})

		go func() {
			for {
				conn, err := ln.Accept()
				if errors.Is(err, client.ErrClosed) {
					return
				}
				assert.NoError(t, err)

				if _, err := conn.Write([]byte(group)); err != nil {
					return
				}
				conn.Close()
			}
		}()
	}
	listen("branch-1")
	listen("branch-2")

	dialer := client.Dialer{
		URL: &url.URL{
			Scheme: "http",
			Host:   node.ProxyAddr(),
		},
	}

	dial := func(group string) string {
		conn, err := dialer.Dial(
			context.TODO(), "my-endpoint", client.WithGroup(group),
		)
		if !assert.NoError(t, err) {
			return ""
		}
		defer conn.Close()

		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		assert.NoError(t, err)
		return string(buf[:n])
	}

	// A connection addressed to a group is served by that group alone.

	assert.Equal(t, "branch-1", dial("branch-1"))
	assert.Equal(t, "branch-2", dial("branch-2"))

	// A group nobody has joined refuses the connection, rather than falling
	// back to the endpoint.

	_, err := dialer.Dial(
		context.TODO(), "my-endpoint", client.WithGroup("branch-3"),
	)
	assert.ErrorContains(t, err, "no available upstreams")
}

// Tests proxy authentication.
func TestProxy_Auth(t *testing.T) {
	endpointClaims := auth.JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Piko: auth.PikoClaims{
			Endpoints: []string{"my-endpoint"},
		},
	}

	// Tests a client authenticating with a valid token.
	t.Run("valid", func(t *testing.T) {
		secretKey := generateTestHSKey()
		node := cluster.NewNode(cluster.WithAuthConfig(auth.Config{
			HMACSecretKey: string(secretKey),
		}))
		node.Start()
		defer node.Stop()

		token := jwt.NewWithClaims(jwt.SigningMethodHS512, endpointClaims)
		tokenString, err := token.SignedString([]byte(secretKey))
		assert.NoError(t, err)

		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+node.ProxyAddr(),
			nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		req.Header.Add("Authorization", "Bearer "+tokenString)
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		// We still haven't added any upstreams so expecte to get a 502 rather
		// than a 200.
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})

	// Tests an client authenticating with an invalid token (signed by
	// the wrong key).
	t.Run("invalid", func(t *testing.T) {
		secretKey := generateTestHSKey()
		node := cluster.NewNode(cluster.WithAuthConfig(auth.Config{
			HMACSecretKey: string(secretKey),
		}))
		node.Start()
		defer node.Stop()

		token := jwt.NewWithClaims(jwt.SigningMethodHS512, endpointClaims)
		tokenString, err := token.SignedString([]byte("invalid-key"))
		assert.NoError(t, err)

		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+node.ProxyAddr(),
			nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		req.Header.Add("Authorization", "Bearer "+tokenString)
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	// Tests an unauthenticated upstream attempting to connect.
	t.Run("unauthenticated", func(t *testing.T) {
		node := cluster.NewNode(cluster.WithAuthConfig(auth.Config{
			HMACSecretKey: string(generateTestHSKey()),
		}))
		node.Start()
		defer node.Stop()

		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+node.ProxyAddr(),
			nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		panic("read rand: " + err.Error())
	}
	return b
}
