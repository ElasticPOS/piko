package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"

	"github.com/andydunstall/piko/pkg/websocket"
)

// Dialer manages opening connections to upstream endpoints.
//
// Note when using HTTP, you can open a TCP direction directly to Piko without
// using a Piko client as the endpoint to connect to is specified in the HTTP
// request. You only need to use [Dialer] when using 'raw' TCP so the client
// can specify which endpoint to connect to.
type Dialer struct {
	// URL is the URL of the Piko server to connect to.
	//
	// This must be the URL of the 'proxy' port. Defaults to
	// 'http://localhost:8000'.
	URL *url.URL

	// Token configures the API key token to authenticate the listener with the
	// Piko server.
	//
	// Defaults to no authentication.
	Token string

	// TLSConfig specifies the TLS configuration to use with the Piko server.
	//
	// If nil, the default configuration is used.
	TLSConfig *tls.Config
}

type dialOptions struct {
	group string
}

// DialOption configures how a connection to an endpoint is opened.
type DialOption interface {
	apply(*dialOptions)
}

type groupOption string

func (o groupOption) apply(opts *dialOptions) {
	opts.group = string(o)
}

// WithGroup connects to the given group of upstreams within the endpoint,
// such as the branch the machines sit in.
//
// The connection is load balanced among the upstreams in that group alone. If
// no upstream has joined the group the connection is refused, rather than
// falling back to the endpoint's other upstreams.
//
// Defaults to connecting to any upstream on the endpoint, grouped or not.
func WithGroup(group string) DialOption {
	return groupOption(group)
}

// Dial opens a TCP connection to the endpoint with the given ID and
// returns a [net.Conn].
func (d *Dialer) Dial(
	ctx context.Context,
	endpointID string,
	opts ...DialOption,
) (net.Conn, error) {
	options := dialOptions{}
	for _, o := range opts {
		o.apply(&options)
	}

	// Dialing is simply opening a WebSocket connection to the target endpoint,
	// then wrapping the WebSocket in a net.Conn.
	return websocket.Dial(
		ctx,
		d.dialURL(endpointID, options.group),
		websocket.WithToken(d.Token),
		websocket.WithTLSConfig(d.TLSConfig),
	)
}

func (d *Dialer) dialURL(endpointID string, group string) string {
	var dialURL url.URL
	if d.URL == nil {
		dialURL = url.URL{
			Scheme: "http",
			Host:   "localhost:8000",
		}
	} else {
		dialURL = *d.URL
	}

	// Add the dial path to the URL.
	dialURL.Path += "/_piko/v1/tcp/" + endpointID

	// Unlike HTTP, which addresses a group with a path prefix, there is no
	// request to carry the group so it's named by the dial URL.
	if group != "" {
		query := dialURL.Query()
		query.Set("group", group)
		dialURL.RawQuery = query.Encode()
	}

	// Set the scheme to WebSocket.
	if dialURL.Scheme == "http" {
		dialURL.Scheme = "ws"
	}
	if dialURL.Scheme == "https" {
		dialURL.Scheme = "wss"
	}

	return dialURL.String()
}
