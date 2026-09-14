package upstream

import (
	"crypto/tls"
	"errors"
	"net"
	"time"

	"github.com/andydunstall/yamux"

	"github.com/andydunstall/piko/server/cluster"
)

var (
	// ErrGone indicates an upstream is no longer accepting connections.
	ErrGone = errors.New("gone")
)

// Upstream represents an upstream for a given endpoint.
//
// An upstream may be an upstream service connected to the local node, or
// another Piko server node.
type Upstream interface {
	EndpointID() string
	// Group returns the group within the endpoint the upstream registered
	// itself in, or an empty string if the upstream isn't grouped.
	//
	// Requests addressed to a group are only load balanced among the
	// upstreams in that group (see Manager.Select).
	Group() string
	// Dial opens a connection the the upstream.
	//
	// If the upstream signals it is no longer accepting connections, returns
	// ErrGone.
	Dial() (net.Conn, error)
	// Forward indicates whether the upstream is forwarding traffic to a remote
	// node rather than a client listener.
	Forward() bool
}

// ConnMetadata describes an upstream service connected to the local node.
//
// Several clients may listen on the same endpoint, so this describes which
// client a connection belongs to when inspecting an endpoint.
type ConnMetadata struct {
	// Name is the name the client gave itself, such as the hostname of the
	// machine it runs on.
	//
	// The name is client supplied and optional, so it may be empty and is not
	// unique.
	Name string `json:"name,omitempty"`

	// Group is the group within the endpoint the client registered itself in,
	// such as the branch its machine sits in.
	//
	// The group is client supplied and optional, so it may be empty.
	Group string `json:"group,omitempty"`

	// Addr is the client address the connection came from.
	Addr string `json:"addr,omitempty"`

	// ConnectedAt is when the client connected, as a Unix timestamp in
	// milliseconds.
	ConnectedAt int64 `json:"connected_at,omitempty"`
}

// ConnUpstream represents a connection to an upstream service thats connected
// to the local node.
type ConnUpstream struct {
	endpointID  string
	group       string
	sess        *yamux.Session
	name        string
	addr        string
	connectedAt time.Time
}

func NewConnUpstream(
	endpointID string,
	group string,
	sess *yamux.Session,
	name string,
	addr string,
) *ConnUpstream {
	return &ConnUpstream{
		endpointID:  endpointID,
		group:       group,
		sess:        sess,
		name:        name,
		addr:        addr,
		connectedAt: time.Now(),
	}
}

// Metadata describes the client that opened the connection.
func (u *ConnUpstream) Metadata() ConnMetadata {
	return ConnMetadata{
		Name:        u.name,
		Group:       u.group,
		Addr:        u.addr,
		ConnectedAt: u.connectedAt.UnixMilli(),
	}
}

func (u *ConnUpstream) EndpointID() string {
	return u.endpointID
}

func (u *ConnUpstream) Group() string {
	return u.group
}

func (u *ConnUpstream) Dial() (net.Conn, error) {
	c, err := u.sess.OpenStream()
	if err != nil && errors.Is(err, yamux.ErrRemoteGoAway) {
		err = ErrGone
	}
	return c, err
}

func (u *ConnUpstream) Forward() bool {
	return false
}

// NodeUpstream represents a remote Piko server node.
type NodeUpstream struct {
	endpointID string
	group      string
	node       *cluster.Node
	tlsConfig  *tls.Config
}

func NewNodeUpstream(
	endpointID string,
	group string,
	node *cluster.Node,
	tlsConfig *tls.Config,
) *NodeUpstream {
	return &NodeUpstream{
		endpointID: endpointID,
		group:      group,
		node:       node,
		tlsConfig:  tlsConfig,
	}
}

func (u *NodeUpstream) EndpointID() string {
	return u.endpointID
}

func (u *NodeUpstream) Group() string {
	return u.group
}

func (u *NodeUpstream) Dial() (net.Conn, error) {
	if u.tlsConfig != nil {
		return tls.Dial("tcp", u.node.ProxyAddr, u.tlsConfig)
	}

	return net.Dial("tcp", u.node.ProxyAddr)
}

func (u *NodeUpstream) Forward() bool {
	return true
}
