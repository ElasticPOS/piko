//go:build system

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/andydunstall/piko/client"
	"github.com/andydunstall/piko/pikotest/cluster"
	"github.com/andydunstall/piko/pikotest/cluster/config"
)

// Tests proxying traffic across multiple Piko server nodes.
func TestCluster_Proxy(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		manager := cluster.NewManager()
		defer manager.Close()

		manager.Update(&config.Config{
			Nodes: 3,
		})

		remoteEndpointCh := make(chan string, 1)
		manager.Nodes()[1].ClusterState().OnRemoteEndpointUpdate(
			func(_ string, endpointID string) {
				remoteEndpointCh <- endpointID
			},
		)

		// Add upstream listener with a HTTP server returning 200.

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   manager.Nodes()[0].UpstreamAddr(),
			},
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		server := httptest.NewUnstartedServer(http.HandlerFunc(
			func(_ http.ResponseWriter, _ *http.Request) {},
		))
		server.Listener = ln
		go server.Start()
		defer server.Close()

		// Wait for node 2 to learn about the new upstream.
		assert.Equal(t, "my-endpoint", <-remoteEndpointCh)

		// Send a request to the upstream via Piko.

		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+manager.Nodes()[1].ProxyAddr(),
			nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// Tests proxying to a group of upstreams held by another node.
	t.Run("group", func(t *testing.T) {
		manager := cluster.NewManager()
		defer manager.Close()

		manager.Update(&config.Config{
			Nodes: 3,
		})

		// A group is gossiped as a route of its own, so node 2 learns about
		// both the endpoint and the group within it.
		remoteEndpointCh := make(chan string, 8)
		manager.Nodes()[1].ClusterState().OnRemoteEndpointUpdate(
			func(_ string, endpointID string) {
				remoteEndpointCh <- endpointID
			},
		)

		// Add an upstream listener in group 'branch-1' whose HTTP server
		// echoes the path it was asked for.

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   manager.Nodes()[0].UpstreamAddr(),
			},
			Group: "branch-1",
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		server := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if _, err := w.Write([]byte(r.URL.Path)); err != nil {
					panic("write bytes: " + err.Error())
				}
			},
		))
		server.Listener = ln
		go server.Start()
		defer server.Close()

		// Wait for node 2 to learn about the group.
		for endpointID := range remoteEndpointCh {
			if endpointID == "my-endpoint/branch-1" {
				break
			}
		}

		// Send a request to node 2, which forwards it to the node holding the
		// group.

		req, _ := http.NewRequest(
			http.MethodGet,
			"http://"+manager.Nodes()[1].ProxyAddr()+"/_g/branch-1/foo",
			nil,
		)
		req.Header.Add("x-piko-endpoint", "my-endpoint")
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// The group prefix is stripped by the node holding the upstream, not
		// by the node that forwarded the request.
		body, err := io.ReadAll(resp.Body)
		assert.NoError(t, err)
		assert.Equal(t, "/foo", string(body))
	})

	t.Run("tcp", func(t *testing.T) {
		manager := cluster.NewManager()
		defer manager.Close()

		manager.Update(&config.Config{
			Nodes: 3,
		})

		remoteEndpointCh := make(chan string, 1)
		manager.Nodes()[1].ClusterState().OnRemoteEndpointUpdate(
			func(_ string, endpointID string) {
				remoteEndpointCh <- endpointID
			},
		)

		// Create a client connecting to node 1 for the upstream listener and
		// node 2 for the proxy connection.
		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   manager.Nodes()[0].UpstreamAddr(),
			},
		}
		dialer := client.Dialer{
			URL: &url.URL{
				Scheme: "http",
				Host:   manager.Nodes()[1].ProxyAddr(),
			},
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)

		// Wait for node 2 to learn about the new upstream.
		assert.Equal(t, "my-endpoint", <-remoteEndpointCh)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := ln.Accept()
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
		}()

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
