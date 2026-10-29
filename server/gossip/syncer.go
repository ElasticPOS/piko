package gossip

import (
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/andydunstall/piko/pkg/gossip"
	"github.com/andydunstall/piko/pkg/log"
	"github.com/andydunstall/piko/server/cluster"
)

type gossiper interface {
	UpsertLocal(key, value string)
	DeleteLocal(key string)
}

// syncer handles syncronising state between gossip and the cluster.
//
// When a node joins, it is considered 'pending' so not added to the cluster
// until we have the full node state. Since gossip propagates state updates in
// order, we only add a node to the cluster when we have the required immutable
// fields.
type syncer struct {
	// pendingNodes contains nodes that we haven't received the full state for
	// yet so can't be added to the cluster.
	pendingNodes map[string]*cluster.Node

	// mu protects the above fields.
	mu sync.Mutex

	clusterState *cluster.State

	gossiper gossiper

	logger log.Logger
}

func newSyncer(clusterState *cluster.State, logger log.Logger) *syncer {
	return &syncer{
		pendingNodes: make(map[string]*cluster.Node),
		clusterState: clusterState,
		logger:       logger,
	}
}

func (s *syncer) Sync(gossiper gossiper) {
	s.gossiper = gossiper

	s.clusterState.OnLocalEndpointUpdate(s.onLocalEndpointUpdate)
	s.clusterState.OnLocalGroupUpdate(s.onLocalGroupUpdate)

	localNode := s.clusterState.LocalNode()
	// First add immutable fields.
	s.gossiper.UpsertLocal("proxy_addr", localNode.ProxyAddr)
	s.gossiper.UpsertLocal("admin_addr", localNode.AdminAddr)
	// Finally add mutable fields.
	for endpointID, listeners := range localNode.Endpoints {
		key := endpointKeyPrefix + endpointID
		s.gossiper.UpsertLocal(key, strconv.Itoa(listeners))
	}
	for groupKey, listeners := range localNode.Groups {
		key := groupKeyPrefix + groupKey
		s.gossiper.UpsertLocal(key, strconv.Itoa(listeners))
	}
}

func (s *syncer) OnJoin(nodeID string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node joined; same id as local node",
			zap.String("node-id", nodeID),
		)
		return
	}

	if _, ok := s.clusterState.Node(nodeID); ok {
		s.logger.Warn(
			"node joined; already in cluster",
			zap.String("node-id", nodeID),
		)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pendingNodes[nodeID]; ok {
		s.logger.Warn(
			"node joined; already pending",
			zap.String("node-id", nodeID),
		)
		return
	}

	// Add as pending since we don't have enough information to add to the
	// cluster.
	s.pendingNodes[nodeID] = &cluster.Node{
		ID: nodeID,
	}

	s.logger.Info("node joined", zap.String("node-id", nodeID))
}

func (s *syncer) OnLeave(nodeID string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node healthy; same id as local node",
			zap.String("node-id", nodeID),
		)
		return
	}

	if updated := s.clusterState.UpdateRemoteStatus(nodeID, cluster.NodeStatusLeft); updated {
		s.logger.Info(
			"node leave; updated cluster",
			zap.String("node-id", nodeID),
		)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// If a pending node has left it can be discarded.
	_, ok := s.pendingNodes[nodeID]
	if ok {
		delete(s.pendingNodes, nodeID)

		s.logger.Info(
			"node leave; removed from pending",
			zap.String("node-id", nodeID),
		)
	} else {
		s.logger.Warn(
			"node left; unknown node",
			zap.String("node-id", nodeID),
		)
	}
}

func (s *syncer) OnReachable(nodeID string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node reachable; same id as local node",
			zap.String("node-id", nodeID),
		)
		return
	}

	if updated := s.clusterState.UpdateRemoteStatus(nodeID, cluster.NodeStatusActive); updated {
		s.logger.Info(
			"node reachable; updated cluster",
			zap.String("node-id", nodeID),
		)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pending, ok := s.pendingNodes[nodeID]
	if ok {
		pending.Status = cluster.NodeStatusActive

		s.logger.Info(
			"node reachable; updated pending",
			zap.String("node-id", nodeID),
		)
	} else {
		s.logger.Warn(
			"node reachable; unknown node",
			zap.String("node-id", nodeID),
		)
	}
}

func (s *syncer) OnUnreachable(nodeID string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node unreachable; same id as local node",
			zap.String("node-id", nodeID),
		)
		return
	}

	if updated := s.clusterState.UpdateRemoteStatus(
		nodeID, cluster.NodeStatusUnreachable,
	); updated {
		s.logger.Info(
			"node unreachable; updated cluster",
			zap.String("node-id", nodeID),
		)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Update pending status. We must still retain the pending node as it may
	// come back.
	pending, ok := s.pendingNodes[nodeID]
	if ok {
		pending.Status = cluster.NodeStatusUnreachable

		s.logger.Info(
			"node unreachable; updated pending",
			zap.String("node-id", nodeID),
		)
	} else {
		s.logger.Warn(
			"node unreachable; unknown node",
			zap.String("node-id", nodeID),
		)
	}
}

func (s *syncer) OnExpired(nodeID string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node expired; same id as local node",
			zap.String("node-id", nodeID),
		)
		return
	}

	if removed := s.clusterState.RemoveNode(nodeID); removed {
		s.logger.Info(
			"node expired; removed from cluster",
			zap.String("node-id", nodeID),
		)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.pendingNodes[nodeID]
	if ok {
		delete(s.pendingNodes, nodeID)

		s.logger.Info(
			"node expired; removed from pending",
			zap.String("node-id", nodeID),
		)
	} else {
		s.logger.Warn(
			"node expired; unknown node",
			zap.String("node-id", nodeID),
		)
	}
}

func (s *syncer) OnUpsertKey(nodeID, key, value string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node upsert state; same id as local node",
			zap.String("node-id", nodeID),
			zap.String("key", key),
		)
		return
	}

	if key == "proxy_addr" || key == "admin_addr" {
		// Ignore immutable fields if the node is in the cluster state. This
		// may occur after a compaction so immutable fields are re-versioned.
		if _, ok := s.clusterState.Node(nodeID); ok {
			return
		}
	}

	// First check if the node is already in the cluster. Only check mutable
	// fields.
	route, routeKey, isRoute := parseRouteKey(key)
	var listeners int
	if isRoute {
		var err error
		listeners, err = strconv.Atoi(value)
		if err != nil {
			s.logger.Error(
				"node upsert state; invalid endpoint listeners",
				zap.String("node-id", nodeID),
				zap.String("listeners", value),
				zap.Error(err),
			)
			return
		}
		if route == groupKeyPrefix {
			if s.clusterState.UpdateRemoteGroup(nodeID, routeKey, listeners) {
				return
			}
		} else if s.clusterState.UpdateRemoteEndpoint(nodeID, routeKey, listeners) {
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.pendingNodes[nodeID]
	if !ok {
		s.logger.Warn(
			"node upsert state; unknown node",
			zap.String("node-id", nodeID),
			zap.String("key", key),
			zap.String("value", value),
		)
		return
	} else if key == "proxy_addr" {
		node.ProxyAddr = value
	} else if key == "admin_addr" {
		node.AdminAddr = value
	} else if isRoute && route == groupKeyPrefix {
		if node.Groups == nil {
			node.Groups = make(map[string]int)
		}
		node.Groups[routeKey] = listeners
	} else if isRoute {
		if node.Endpoints == nil {
			node.Endpoints = make(map[string]int)
		}
		node.Endpoints[routeKey] = listeners
	} else {
		s.logger.Error(
			"node upsert state; unsupported key",
			zap.String("node-id", nodeID),
			zap.String("key", key),
		)
		return
	}

	// Once we have the nodes immutable fields it can be added to the cluster.
	if node.ProxyAddr != "" && node.AdminAddr != "" {
		if node.Status == "" {
			// Unless we've received a unreachable/leave notification, we
			// consider the node as active.
			node.Status = cluster.NodeStatusActive
		}

		delete(s.pendingNodes, node.ID)
		s.clusterState.AddNode(node)

		s.logger.Debug(
			"node upsert state; added to cluster",
			zap.String("node-id", nodeID),
			zap.String("key", key),
			zap.String("value", value),
		)
	} else {
		s.logger.Debug(
			"node upsert state; updated pending node",
			zap.String("node-id", nodeID),
			zap.String("key", key),
			zap.String("value", value),
		)
	}
}

func (s *syncer) OnDeleteKey(nodeID, key string) {
	if nodeID == s.clusterState.LocalID() {
		s.logger.Warn(
			"node delete state; same id as local node",
			zap.String("node-id", nodeID),
			zap.String("key", key),
		)
		return
	}

	// Only endpoint and group state can be deleted.
	route, routeKey, isRoute := parseRouteKey(key)
	if !isRoute {
		s.logger.Error(
			"node delete state; unsupported key",
			zap.String("node-id", nodeID),
			zap.String("key", key),
		)
		return
	}

	var removed bool
	if route == groupKeyPrefix {
		removed = s.clusterState.RemoveRemoteGroup(nodeID, routeKey)
	} else {
		removed = s.clusterState.RemoveRemoteEndpoint(nodeID, routeKey)
	}
	if removed {
		s.logger.Debug(
			"node delete state; cluster updated",
			zap.String("node-id", nodeID),
			zap.String("key", key),
		)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.pendingNodes[nodeID]
	if !ok {
		s.logger.Warn(
			"node delete state; unknown node",
			zap.String("node-id", nodeID),
			zap.String("key", key),
		)
		return
	}

	if route == groupKeyPrefix {
		delete(node.Groups, routeKey)
	} else {
		delete(node.Endpoints, routeKey)
	}

	s.logger.Debug(
		"node delete state; pending node",
		zap.String("node-id", nodeID),
		zap.String("key", key),
	)
}

func (s *syncer) onLocalEndpointUpdate(endpointID string) {
	s.syncLocalRoute(
		endpointKeyPrefix+endpointID,
		s.clusterState.LocalEndpointListeners(endpointID),
	)
}

func (s *syncer) onLocalGroupUpdate(groupKey string) {
	s.syncLocalRoute(
		groupKeyPrefix+groupKey,
		s.clusterState.LocalGroupListeners(groupKey),
	)
}

func (s *syncer) syncLocalRoute(key string, listeners int) {
	if listeners > 0 {
		s.gossiper.UpsertLocal(key, strconv.Itoa(listeners))
	} else {
		s.gossiper.DeleteLocal(key)
	}
}

const (
	// endpointKeyPrefix prefixes the gossip key of an active endpoint, whose
	// value is the number of listeners on the endpoint.
	endpointKeyPrefix = "endpoint:"

	// groupKeyPrefix prefixes the gossip key of an active group within an
	// endpoint (see cluster.GroupKey), whose value is the number of listeners
	// in the group.
	//
	// Groups have a key of their own, rather than being gossiped as
	// endpoints, as their listeners are also counted under their endpoint.
	groupKeyPrefix = "group:"
)

// parseRouteKey splits a gossip key for an endpoint or group into its prefix
// and the endpoint ID or group key. Returns false if the key isn't for an
// endpoint or group.
func parseRouteKey(key string) (prefix string, routeKey string, ok bool) {
	for _, prefix := range []string{endpointKeyPrefix, groupKeyPrefix} {
		if routeKey, ok := strings.CutPrefix(key, prefix); ok {
			return prefix, routeKey, true
		}
	}
	return "", "", false
}

var _ gossip.Watcher = &syncer{}
