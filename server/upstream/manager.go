package upstream

import (
	"crypto/tls"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andydunstall/piko/server/cluster"
)

// Manager manages the upstream routes for each endpoint.
//
// This includes upstreams connected to the local node, or other server nodes
// in the cluster with a connected upstream for the target endpoint.
type Manager interface {
	// Select looks up an upstream for the given endpoint ID.
	//
	// This will first look for an upstream connected to the local node, and
	// load balance among the available connected upstreams.
	//
	// If there are no upstreams connected for the endpoint, and 'allowForward'
	// is true, it will look for another node in the cluster that has an
	// upstream connection for the endpoint and use that node as the upstream.
	//
	// If 'group' is given, only upstreams that registered themselves in that
	// group are considered. If it's empty, any upstream for the endpoint may
	// be selected, whether it's grouped or not.
	Select(endpointID string, group string, allowForward bool) (Upstream, bool)

	// AddConn adds a local upstream connection.
	AddConn(u Upstream)

	// RemoveConn removes a local upstream connection.
	RemoveConn(u Upstream)
}

// loadBalancer load balances requests among upstreams in a round-robin
// fashion.
type loadBalancer struct {
	upstreams []Upstream
	nextIndex int
}

func (lb *loadBalancer) Add(u Upstream) {
	lb.upstreams = append(lb.upstreams, u)
}

func (lb *loadBalancer) Remove(u Upstream) bool {
	for i := 0; i != len(lb.upstreams); i++ {
		if lb.upstreams[i] != u {
			continue
		}
		lb.upstreams = append(lb.upstreams[:i], lb.upstreams[i+1:]...)
		if len(lb.upstreams) == 0 {
			return true
		}
		lb.nextIndex %= len(lb.upstreams)
		return false
	}
	return len(lb.upstreams) == 0
}

func (lb *loadBalancer) Next() Upstream {
	if len(lb.upstreams) == 0 {
		return nil
	}

	u := lb.upstreams[lb.nextIndex]
	lb.nextIndex++
	lb.nextIndex %= len(lb.upstreams)
	return u
}

type LoadBalancedManager struct {
	localUpstreams map[string]*loadBalancer

	// localGroups contains the grouped subset of localUpstreams, keyed by
	// groupKey. Grouped upstreams appear in both, as an ungrouped request must
	// still reach them.
	localGroups map[string]*loadBalancer

	mu sync.Mutex

	cluster *cluster.State

	metrics *Metrics

	tlsConfig *tls.Config
}

func NewLoadBalancedManager(cluster *cluster.State, proxyClientTLSConfig *tls.Config) *LoadBalancedManager {
	return &LoadBalancedManager{
		localUpstreams: make(map[string]*loadBalancer),
		localGroups:    make(map[string]*loadBalancer),
		cluster:        cluster,
		tlsConfig:      proxyClientTLSConfig,
		metrics:        NewMetrics(),
	}
}

func (m *LoadBalancedManager) Select(
	endpointID string,
	group string,
	allowRemote bool,
) (Upstream, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Groups are registered as routes in their own right, both locally and in
	// the cluster, so looking one up is the same lookup under another key.
	key := endpointID
	local := m.localUpstreams
	if group != "" {
		key = groupKey(endpointID, group)
		local = m.localGroups
	}

	lb, ok := local[key]
	if ok {
		m.metrics.UpstreamRequestsTotal.Inc()
		return lb.Next(), true
	}
	if !allowRemote {
		return nil, false
	}

	node, ok := m.cluster.LookupEndpoint(key)
	if !ok {
		return nil, false
	}
	m.metrics.RemoteRequestsTotal.With(prometheus.Labels{
		"node_id": node.ID,
	}).Inc()
	return NewNodeUpstream(endpointID, group, node, m.tlsConfig), true
}

func (m *LoadBalancedManager) AddConn(u Upstream) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lb, ok := m.localUpstreams[u.EndpointID()]
	if !ok {
		lb = &loadBalancer{}

		m.metrics.RegisteredEndpoints.Inc()
	}

	lb.Add(u)
	m.localUpstreams[u.EndpointID()] = lb

	m.cluster.AddLocalEndpoint(u.EndpointID())

	if group := u.Group(); group != "" {
		key := groupKey(u.EndpointID(), group)

		lb, ok := m.localGroups[key]
		if !ok {
			lb = &loadBalancer{}
		}
		lb.Add(u)
		m.localGroups[key] = lb

		m.cluster.AddLocalEndpoint(key)
	}

	m.metrics.ConnectedUpstreams.Inc()
}

func (m *LoadBalancedManager) RemoveConn(u Upstream) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lb, ok := m.localUpstreams[u.EndpointID()]
	if !ok {
		return
	}
	if lb.Remove(u) {
		delete(m.localUpstreams, u.EndpointID())

		m.metrics.RegisteredEndpoints.Dec()
	}

	m.cluster.RemoveLocalEndpoint(u.EndpointID())

	if group := u.Group(); group != "" {
		key := groupKey(u.EndpointID(), group)

		if lb, ok := m.localGroups[key]; ok {
			if lb.Remove(u) {
				delete(m.localGroups, key)
			}

			m.cluster.RemoveLocalEndpoint(key)
		}
	}

	m.metrics.ConnectedUpstreams.Dec()
}

func (m *LoadBalancedManager) Endpoints() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	endpoints := make(map[string]int)
	for endpointID, lb := range m.localUpstreams {
		endpoints[endpointID] = len(lb.upstreams)
	}
	return endpoints
}

// Upstreams returns the upstreams connected to the local node for the given
// endpoint.
//
// Only client connections are returned, as upstreams forwarding to another
// node are never registered locally.
func (m *LoadBalancedManager) Upstreams(endpointID string) []ConnMetadata {
	m.mu.Lock()
	defer m.mu.Unlock()

	lb, ok := m.localUpstreams[endpointID]
	if !ok {
		return []ConnMetadata{}
	}

	upstreams := make([]ConnMetadata, 0, len(lb.upstreams))
	for _, u := range lb.upstreams {
		conn, ok := u.(*ConnUpstream)
		if !ok {
			continue
		}
		upstreams = append(upstreams, conn.Metadata())
	}
	return upstreams
}

// groupKey is the route a group of upstreams within an endpoint is registered
// under.
//
// Groups are registered as routes of their own so a group can be looked up on
// a remote node, as the cluster only gossips endpoint names and their listener
// counts. '/' is used as the separator as an endpoint ID can never contain
// one: it comes from either a host name or a path segment.
func groupKey(endpointID string, group string) string {
	return endpointID + "/" + group
}

func (m *LoadBalancedManager) Metrics() *Metrics {
	return m.metrics
}
