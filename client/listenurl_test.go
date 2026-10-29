package client

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpstream_ListenURL(t *testing.T) {
	serverURL := &url.URL{Scheme: "https", Host: "piko.example.com"}

	t.Run("no name", func(t *testing.T) {
		upstream := &Upstream{URL: serverURL}
		assert.Equal(
			t,
			"wss://piko.example.com/piko/v1/upstream/my-endpoint",
			upstream.listenURL("my-endpoint"),
		)
	})

	t.Run("name", func(t *testing.T) {
		upstream := &Upstream{URL: serverURL, Name: "my-host"}
		assert.Equal(
			t,
			"wss://piko.example.com/piko/v1/upstream/my-endpoint?name=my-host",
			upstream.listenURL("my-endpoint"),
		)
	})

	// The name comes from the machine the client runs on, so it isn't
	// guaranteed to be URL safe.
	t.Run("name escaped", func(t *testing.T) {
		upstream := &Upstream{URL: serverURL, Name: "my host/1 &2"}
		assert.Equal(
			t,
			"wss://piko.example.com/piko/v1/upstream/my-endpoint?name=my+host%2F1+%262",
			upstream.listenURL("my-endpoint"),
		)
	})
}

// Tests an invalid group fails before connecting, rather than being rejected
// by the server.
func TestInvalidGroup(t *testing.T) {
	// Nothing listens on the URL, so only validation can fail fast.
	serverURL := &url.URL{Scheme: "http", Host: "127.0.0.1:1"}

	upstream := &Upstream{URL: serverURL, Group: "branch 1"}
	_, err := upstream.Listen(context.TODO(), "my-endpoint")
	assert.ErrorContains(t, err, "group")
	_, err = upstream.ListenAndForward(context.TODO(), "my-endpoint", "localhost:3000")
	assert.ErrorContains(t, err, "group")

	dialer := &Dialer{URL: serverURL}
	_, err = dialer.Dial(context.TODO(), "my-endpoint", WithGroup("branch/1"))
	assert.ErrorContains(t, err, "group")
}
