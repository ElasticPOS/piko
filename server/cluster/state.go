package cluster

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andydunstall/piko/pkg/log"
)

// State represents the known state of the cluster as seen by the local
// node.
//
// This state is eventually consistent.
type State struct {
	localID string
	nodes   map[string]*Node

	// localSubscribers and remoteSubscribers are indexed by routeKind.
	localSubscribers  [numRouteKinds][]func(key string)
	remoteSubscribers [numRouteKinds][]func(nodeID string, key string)

	// mu protects the above fields.
	mu sync.RWMutex

	metrics *Metrics

	logger log.Logger
}

func NewState(
	localNode *Node,
	logger log.Logger,
) *State {
	// The local node is always active.
	localNode.Status = NodeStatusActive
	nodes := make(map[string]*Node)
	nodes[localNode.ID] = localNode

	s := &State{
		localID: localNode.ID,
		nodes:   nodes,
		metrics: NewMetrics(),
		logger:  logger.WithSubsystem("cluster"),
	}
	s.addMetricsNode(localNode.Status)
	return s
}

// Node returns the known state of the node with the given ID, or false if the
// node is unknown.
func (s *State) Node(id string) (*Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[id]
	if !ok {
		return nil, false
	}
	return node.Copy(), true
}

// LocalID returns the ID of the local node.
func (s *State) LocalID() string {
	// localID is immutable so don't need a mutex.
	return s.localID
}

// LocalNode returns the state of the local node.
func (s *State) LocalNode() *Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[s.localID]
	if !ok {
		panic("local node not in cluster")
	}
	return node.Copy()
}

// Nodes returns the state of the known nodes.
func (s *State) Nodes() []*Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make([]*Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		nodes = append(nodes, node.Copy())
	}
	return nodes
}

// NodesMetadata returns the metadata of the known nodes.
func (s *State) NodesMetadata() []*NodeMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make([]*NodeMetadata, 0, len(s.nodes))
	for _, node := range s.nodes {
		nodes = append(nodes, node.Metadata())
	}
	return nodes
}

// LookupEndpoint looks up a node that the endpoint with the given ID is active
// on.
func (s *State) LookupEndpoint(endpointID string) (*Node, bool) {
	return s.lookup(endpointRoute, endpointID)
}

// LookupGroup looks up a node that the group within the endpoint with the
// given ID is active on.
func (s *State) LookupGroup(endpointID string, group string) (*Node, bool) {
	return s.lookup(groupRoute, GroupKey(endpointID, group))
}

// AddLocalEndpoint adds the active endpoint to the local node state.
func (s *State) AddLocalEndpoint(endpointID string) {
	s.addLocal(endpointRoute, endpointID)
}

// RemoveLocalEndpoint removes the active endpoint from the local node state.
func (s *State) RemoveLocalEndpoint(endpointID string) {
	s.removeLocal(endpointRoute, endpointID)
}

// AddLocalGroup adds the active group within the endpoint to the local node
// state.
//
// Note this doesn't add the endpoint itself, which is added separately with
// AddLocalEndpoint.
func (s *State) AddLocalGroup(endpointID string, group string) {
	s.addLocal(groupRoute, GroupKey(endpointID, group))
}

// RemoveLocalGroup removes the active group within the endpoint from the
// local node state.
func (s *State) RemoveLocalGroup(endpointID string, group string) {
	s.removeLocal(groupRoute, GroupKey(endpointID, group))
}

func (s *State) LocalEndpointListeners(endpointID string) int {
	return s.localListeners(endpointRoute, endpointID)
}

// LocalGroupListeners returns the number of listeners in the group with the
// given key (see GroupKey) on the local node.
func (s *State) LocalGroupListeners(key string) int {
	return s.localListeners(groupRoute, key)
}

// OnLocalEndpointUpdate subscribes to changes to the local nodes active
// endpoints.
//
// The callback is called with the cluster mutex locked so must not block or
// call back to the cluster.
func (s *State) OnLocalEndpointUpdate(f func(endpointID string)) {
	s.onLocalUpdate(endpointRoute, f)
}

// OnLocalGroupUpdate subscribes to changes to the local nodes active groups,
// called with the group's key (see GroupKey).
func (s *State) OnLocalGroupUpdate(f func(key string)) {
	s.onLocalUpdate(groupRoute, f)
}

func (s *State) OnRemoteEndpointUpdate(f func(nodeID string, endpointID string)) {
	s.onRemoteUpdate(endpointRoute, f)
}

// OnRemoteGroupUpdate subscribes to changes to remote nodes active groups,
// called with the group's key (see GroupKey).
func (s *State) OnRemoteGroupUpdate(f func(nodeID string, key string)) {
	s.onRemoteUpdate(groupRoute, f)
}

// AddNode adds the given node to the cluster.
func (s *State) AddNode(node *Node) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if node.ID == s.localID {
		s.logger.Warn("add node: cannot add local node")
		return
	}

	if _, ok := s.nodes[node.ID]; ok {
		// If already in the cluster update the node but warn as this should
		// not happen.
		s.logger.Warn("add node: node already in cluster")
	}

	s.nodes[node.ID] = node
	s.addMetricsNode(node.Status)
}

// RemoveNode removes the node with the given ID from the cluster.
func (s *State) RemoveNode(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == s.localID {
		s.logger.Warn("remove node: cannot remove local node")
		return false
	}

	node, ok := s.nodes[id]
	if !ok {
		s.logger.Warn("remove node: node not in cluster")
		return false
	}

	delete(s.nodes, id)
	s.removeMetricsNode(node.Status)

	return true
}

// UpdateRemoteStatus sets the status of the remote node with the given ID.
func (s *State) UpdateRemoteStatus(id string, status NodeStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == s.localID {
		s.logger.Warn("update remote status: cannot update local node")
		return false
	}

	n, ok := s.nodes[id]
	if !ok {
		s.logger.Warn("update remote status: node not in cluster")
		return false
	}

	oldStatus := n.Status
	n.Status = status
	s.updateMetricsNode(oldStatus, status)
	return true
}

// UpdateRemoteEndpoint sets the number of listeners for the active endpoint
// for the node with the given ID.
func (s *State) UpdateRemoteEndpoint(
	id string,
	endpointID string,
	listeners int,
) bool {
	return s.updateRemote(endpointRoute, id, endpointID, listeners)
}

// RemoveRemoteEndpoint removes the active endpoint from the node with the
// given ID.
func (s *State) RemoveRemoteEndpoint(id string, endpointID string) bool {
	return s.removeRemote(endpointRoute, id, endpointID)
}

// UpdateRemoteGroup sets the number of listeners for the active group with
// the given key (see GroupKey) for the node with the given ID.
func (s *State) UpdateRemoteGroup(id string, key string, listeners int) bool {
	return s.updateRemote(groupRoute, id, key, listeners)
}

// RemoveRemoteGroup removes the active group with the given key (see
// GroupKey) from the node with the given ID.
func (s *State) RemoveRemoteGroup(id string, key string) bool {
	return s.removeRemote(groupRoute, id, key)
}

// AvgConns returns the mean number of connections per node in the cluster.
func (s *State) AvgConns() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var totalConns int
	var nodes int
	for _, node := range s.nodes {
		if node.Status != NodeStatusActive {
			// Ignore unreachable and left nodes.
			continue
		}
		// Only endpoints are counted, as grouped upstreams are also counted
		// under their endpoint.
		for _, conns := range node.Endpoints {
			totalConns += conns
		}
		nodes++
	}

	return totalConns / nodes
}

func (s *State) Metrics() *Metrics {
	return s.metrics
}

func (s *State) lookup(kind routeKind, key string) (*Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, node := range s.nodes {
		if node.ID == s.localID {
			// Ignore ourselves.
			continue
		}
		if node.Status != NodeStatusActive {
			// Ignore unreachable and left nodes.
			continue
		}
		if listeners, ok := node.routes(kind, false)[key]; ok && listeners > 0 {
			return node.Copy(), true
		}
	}

	return nil, false
}

func (s *State) addLocal(kind routeKind, key string) {
	s.mu.Lock()

	node, ok := s.nodes[s.localID]
	if !ok {
		panic("local node not in cluster")
	}

	routes := node.routes(kind, true)
	routes[key] = routes[key] + 1

	subscribers := make([]func(key string), 0, len(s.localSubscribers[kind]))
	subscribers = append(subscribers, s.localSubscribers[kind]...)

	s.mu.Unlock()

	for _, f := range subscribers {
		f(key)
	}
}

func (s *State) removeLocal(kind routeKind, key string) {
	s.mu.Lock()

	node, ok := s.nodes[s.localID]
	if !ok {
		panic("local node not in cluster")
	}

	routes := node.routes(kind, true)

	listeners, ok := routes[key]
	if !ok || listeners == 0 {
		s.logger.Warn("remove local endpoint: endpoint not found")
		s.mu.Unlock()
		return
	}

	if listeners > 1 {
		routes[key] = listeners - 1
	} else {
		delete(routes, key)
	}

	subscribers := make([]func(key string), 0, len(s.localSubscribers[kind]))
	subscribers = append(subscribers, s.localSubscribers[kind]...)

	s.mu.Unlock()

	for _, f := range subscribers {
		f(key)
	}
}

func (s *State) localListeners(kind routeKind, key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[s.localID]
	if !ok {
		panic("local node not in cluster")
	}

	return node.routes(kind, false)[key]
}

func (s *State) onLocalUpdate(kind routeKind, f func(key string)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.localSubscribers[kind] = append(s.localSubscribers[kind], f)
}

func (s *State) onRemoteUpdate(kind routeKind, f func(nodeID string, key string)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.remoteSubscribers[kind] = append(s.remoteSubscribers[kind], f)
}

func (s *State) updateRemote(
	kind routeKind,
	id string,
	key string,
	listeners int,
) bool {
	s.mu.Lock()

	if id == s.localID {
		s.logger.Warn("update remote endpoint: cannot update local node")
		s.mu.Unlock()
		return false
	}

	n, ok := s.nodes[id]
	if !ok {
		s.logger.Warn("update remote endpoint: node not in cluster")
		s.mu.Unlock()
		return false
	}

	n.routes(kind, true)[key] = listeners

	subscribers := make([]func(nodeID string, key string), 0, len(s.remoteSubscribers[kind]))
	subscribers = append(subscribers, s.remoteSubscribers[kind]...)

	s.mu.Unlock()

	for _, f := range subscribers {
		f(id, key)
	}

	return true
}

func (s *State) removeRemote(kind routeKind, id string, key string) bool {
	s.mu.Lock()

	if id == s.localID {
		s.logger.Warn("remove remote endpoint: cannot update local node")
		s.mu.Unlock()
		return false
	}

	n, ok := s.nodes[id]
	if !ok {
		s.logger.Warn("remove remote endpoint: node not in cluster")
		s.mu.Unlock()
		return false
	}

	delete(n.routes(kind, false), key)

	subscribers := make([]func(nodeID string, key string), 0, len(s.remoteSubscribers[kind]))
	subscribers = append(subscribers, s.remoteSubscribers[kind]...)

	s.mu.Unlock()

	for _, f := range subscribers {
		f(id, key)
	}

	return true
}

func (s *State) updateMetricsNode(oldStatus NodeStatus, newStatus NodeStatus) {
	s.removeMetricsNode(oldStatus)
	s.addMetricsNode(newStatus)
}

func (s *State) addMetricsNode(status NodeStatus) {
	s.metrics.Nodes.With(prometheus.Labels{"status": string(status)}).Inc()
}

func (s *State) removeMetricsNode(status NodeStatus) {
	s.metrics.Nodes.With(prometheus.Labels{"status": string(status)}).Dec()
}
