package client

import (
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
