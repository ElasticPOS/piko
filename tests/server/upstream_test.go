//go:build system

package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"

	"github.com/andydunstall/piko/client"
	"github.com/andydunstall/piko/pikotest/cluster"
	"github.com/andydunstall/piko/pkg/auth"
	"github.com/andydunstall/piko/server/upstream"
)

// Tests the status API describes the upstreams connected to an endpoint,
// including the name each client gave itself.
func TestUpstream_Status(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	up := client.Upstream{
		URL: &url.URL{
			Scheme: "http",
			Host:   node.UpstreamAddr(),
		},
		Name: "my-host",
	}
	ln, err := up.Listen(context.TODO(), "my-endpoint")
	assert.NoError(t, err)
	defer ln.Close()

	statusURL := "http://" + node.AdminAddr() +
		"/status/upstream/endpoints/my-endpoint"

	var upstreams []upstream.ConnMetadata
	// The client connects before the server registers the upstream, so wait
	// for it to show up.
	assert.Eventually(t, func() bool {
		resp, err := http.Get(statusURL)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return false
		}
		if err := json.NewDecoder(resp.Body).Decode(&upstreams); err != nil {
			return false
		}
		return len(upstreams) == 1
	}, time.Second*5, time.Millisecond*10)

	assert.Equal(t, "my-host", upstreams[0].Name)
	assert.NotEmpty(t, upstreams[0].Addr)
	assert.NotZero(t, upstreams[0].ConnectedAt)
}

// Tests upstream authentication.
func TestUpstream_Auth(t *testing.T) {
	endpointClaims := auth.JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Piko: auth.PikoClaims{
			Endpoints: []string{"my-endpoint"},
		},
	}

	// Tests an upstream authenticating with a valid token.
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

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
			Token: tokenString,
		}
		ln, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.NoError(t, err)
		defer ln.Close()
	})

	// Tests an upstream authenticating with an invalid token (signed by
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

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
			Token: tokenString,
		}
		_, err = upstream.Listen(context.TODO(), "my-endpoint")
		assert.ErrorContains(t, err, "connect: 401: invalid token")
	})

	// Tests an unauthenticated upstream attempting to connect.
	t.Run("unauthenticated", func(t *testing.T) {
		node := cluster.NewNode(cluster.WithAuthConfig(auth.Config{
			HMACSecretKey: string(generateTestHSKey()),
		}))
		node.Start()
		defer node.Stop()

		upstream := client.Upstream{
			URL: &url.URL{
				Scheme: "http",
				Host:   node.UpstreamAddr(),
			},
		}
		_, err := upstream.Listen(context.TODO(), "my-endpoint")
		assert.ErrorContains(t, err, "connect: 401: missing authorization")
	})
}

func generateTestHSKey() []byte {
	b := make([]byte, 10)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return b
}
