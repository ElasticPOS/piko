package cluster

import (
	"crypto/rand"
	"math/big"
)

var (
	alphaNumericChars = []byte("abcdefghijklmnopqrstuvwxyz1234567890")
)

// routeKind is the kind of route a node advertises its upstream listeners
// under.
type routeKind int

const (
	// endpointRoute routes to any upstream on an endpoint.
	endpointRoute routeKind = iota
	// groupRoute routes to the upstreams in a group within an endpoint.
	groupRoute
	numRouteKinds
)

// NodeStatus contains the known status of a node.
type NodeStatus string

const (
	// NodeStatusActive means the node is healthy and accepting traffic.
	NodeStatusActive NodeStatus = "active"
	// NodeStatusUnreachable means the node is considered unreachable.
	NodeStatusUnreachable NodeStatus = "unreachable"
	// NodeStatusLeft means the node has left the cluster.
	NodeStatusLeft NodeStatus = "left"
)

// Node represents the known state about a node in the cluster.
//
// Note to ensure updates are propagated, never update a node directly, only
// ever update via the NetworkMap.
type Node struct {
	// ID is a unique identifier for the node in the cluster.
	//
	// The ID is immutable.
	ID string `json:"id"`

	// Status contains the known status of the node.
	Status NodeStatus `json:"status"`

	// ProxyAddr is the advertised proxy address.
	//
	// The address is immutable.
	ProxyAddr string `json:"proxy_addr"`

	// AdminAddr is the advertised admin address.
	//
	// The address is immutable.
	AdminAddr string `json:"admin_addr"`

	// Endpoints contains the known active endpoints on the node (endpoints
	// with at least one upstream listener).
	//
	// This maps the endpoint ID to the number of known listeners for that
	// endpoint.
	Endpoints map[string]int `json:"endpoints"`

	// Groups contains the known active groups of upstreams within endpoints on
	// the node (groups with at least one upstream listener).
	//
	// This maps the group's key (see GroupKey) to the number of known
	// listeners in that group. Grouped listeners are also counted under their
	// endpoint in Endpoints, so groups are kept apart to avoid counting them
	// twice.
	Groups map[string]int `json:"groups,omitempty"`
}

// GroupKey identifies a group of upstreams within an endpoint.
//
// '/' is used as the separator as an endpoint ID can never contain one: it
// comes from either a host name or a path segment.
func GroupKey(endpointID string, group string) string {
	return endpointID + "/" + group
}

func (n *Node) Copy() *Node {
	return &Node{
		ID:        n.ID,
		Status:    n.Status,
		ProxyAddr: n.ProxyAddr,
		AdminAddr: n.AdminAddr,
		Endpoints: copyRoutes(n.Endpoints),
		Groups:    copyRoutes(n.Groups),
	}
}

// routes returns the node's routes of the given kind, creating the map if
// 'create' is true and it doesn't exist yet.
func (n *Node) routes(kind routeKind, create bool) map[string]int {
	routes := &n.Endpoints
	if kind == groupRoute {
		routes = &n.Groups
	}
	if *routes == nil && create {
		*routes = make(map[string]int)
	}
	return *routes
}

func copyRoutes(routes map[string]int) map[string]int {
	if len(routes) == 0 {
		return nil
	}
	c := make(map[string]int, len(routes))
	for key, listeners := range routes {
		c[key] = listeners
	}
	return c
}

func (n *Node) Metadata() *NodeMetadata {
	upstreams := 0
	for _, endpointUpstreams := range n.Endpoints {
		upstreams += endpointUpstreams
	}
	return &NodeMetadata{
		ID:        n.ID,
		Status:    n.Status,
		ProxyAddr: n.ProxyAddr,
		AdminAddr: n.AdminAddr,
		Endpoints: len(n.Endpoints),
		Upstreams: upstreams,
	}
}

// NodeMetadata contains metadata fields from Node.
type NodeMetadata struct {
	ID        string     `json:"id"`
	Status    NodeStatus `json:"status"`
	ProxyAddr string     `json:"proxy_addr"`
	AdminAddr string     `json:"admin_addr"`
	Endpoints int        `json:"endpoints"`
	// Upstreams is the number of upstreams connected to this node.
	Upstreams int `json:"upstreams"`
}

func GenerateNodeID() string {
	b := make([]byte, 7)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphaNumericChars))))
		if err != nil {
			// We don't expect to ever get an error so panic rather than try to
			// handle.
			panic("failed to generate random number: " + err.Error())
		}
		b[i] = alphaNumericChars[n.Int64()]
	}
	return string(b)
}
