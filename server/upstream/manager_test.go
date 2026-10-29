package upstream

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andydunstall/piko/pkg/log"
	"github.com/andydunstall/piko/server/cluster"
)

type fakeUpstream struct {
	endpointID string
	group      string
}

func (u *fakeUpstream) EndpointID() string {
	return u.endpointID
}

func (u *fakeUpstream) Group() string {
	return u.group
}

func (u *fakeUpstream) Dial() (net.Conn, error) {
	return nil, nil
}

func (u *fakeUpstream) Forward() bool {
	return false
}

func TestLocalLoadBalancer(t *testing.T) {
	lb := &loadBalancer{}

	assert.Nil(t, lb.Next())

	u1 := &fakeUpstream{endpointID: "1"}
	lb.Add(u1)
	assert.Equal(t, "1", lb.Next().EndpointID())

	u2 := &fakeUpstream{endpointID: "2"}
	u3 := &fakeUpstream{endpointID: "3"}
	u4 := &fakeUpstream{endpointID: "4"}
	lb.Add(u2)
	lb.Add(u3)
	lb.Add(u4)

	assert.Equal(t, "1", lb.Next().EndpointID())
	assert.Equal(t, "2", lb.Next().EndpointID())
	assert.Equal(t, "3", lb.Next().EndpointID())
	assert.Equal(t, "4", lb.Next().EndpointID())
	assert.Equal(t, "1", lb.Next().EndpointID())
	assert.Equal(t, "2", lb.Next().EndpointID())
	assert.Equal(t, "3", lb.Next().EndpointID())

	assert.True(t, lb.Remove(u2))
	assert.True(t, lb.Remove(u3))
	assert.Equal(t, "1", lb.Next().EndpointID())
	assert.Equal(t, "4", lb.Next().EndpointID())
	assert.Equal(t, "1", lb.Next().EndpointID())

	assert.True(t, lb.Remove(u1))
	assert.False(t, lb.Empty())
	assert.True(t, lb.Remove(u4))
	assert.True(t, lb.Empty())

	// Removing an upstream that's already gone is a no-op.
	assert.False(t, lb.Remove(u4))

	assert.Nil(t, lb.Next())
}

// Tests grouped upstreams only serve requests addressed to their group,
// though a request addressed to the endpoint still reaches them.
func TestLoadBalancedManager_SelectGroup(t *testing.T) {
	m := NewLoadBalancedManager(
		cluster.NewState(&cluster.Node{ID: "local"}, log.NewNopLogger()),
		nil,
	)

	branch1 := &fakeUpstream{endpointID: "my-endpoint", group: "branch-1"}
	branch2 := &fakeUpstream{endpointID: "my-endpoint", group: "branch-2"}
	ungrouped := &fakeUpstream{endpointID: "my-endpoint"}
	m.AddConn(branch1)
	m.AddConn(branch2)
	m.AddConn(ungrouped)

	// A request addressed to a group only reaches the upstreams in that
	// group.
	for i := 0; i != 3; i++ {
		u, ok := m.Select("my-endpoint", "branch-1", false)
		require.True(t, ok)
		assert.Equal(t, branch1, u)
	}

	// A request addressed to the endpoint reaches every upstream, grouped or
	// not.
	selected := make(map[Upstream]struct{})
	for i := 0; i != 3; i++ {
		u, ok := m.Select("my-endpoint", "", false)
		require.True(t, ok)
		selected[u] = struct{}{}
	}
	assert.Len(t, selected, 3)

	// A group nobody registered has no upstreams, rather than falling back to
	// the endpoint.
	_, ok := m.Select("my-endpoint", "branch-3", false)
	assert.False(t, ok)

	// Removing the last upstream in a group removes the group, leaving the
	// endpoint's remaining upstreams.
	m.RemoveConn(branch1)
	_, ok = m.Select("my-endpoint", "branch-1", false)
	assert.False(t, ok)
	_, ok = m.Select("my-endpoint", "branch-2", false)
	assert.True(t, ok)
	_, ok = m.Select("my-endpoint", "", false)
	assert.True(t, ok)

	m.RemoveConn(branch2)
	m.RemoveConn(ungrouped)
	_, ok = m.Select("my-endpoint", "", false)
	assert.False(t, ok)
	assert.Empty(t, m.Endpoints())
}

// Tests the manager describes the clients connected to the local node for an
// endpoint.
func TestLoadBalancedManager_Upstreams(t *testing.T) {
	m := NewLoadBalancedManager(
		cluster.NewState(&cluster.Node{ID: "local"}, log.NewNopLogger()),
		nil,
	)

	assert.Empty(t, m.Upstreams("my-endpoint"))

	named := NewConnUpstream("my-endpoint", "", nil, "my-host", "10.26.0.1")
	// Clients aren't required to send a name.
	unnamed := NewConnUpstream("my-endpoint", "", nil, "", "10.26.0.2")
	m.AddConn(named)
	m.AddConn(unnamed)

	upstreams := m.Upstreams("my-endpoint")
	require.Len(t, upstreams, 2)
	assert.Equal(t, "my-host", upstreams[0].Name)
	assert.Equal(t, "10.26.0.1", upstreams[0].Addr)
	assert.NotZero(t, upstreams[0].ConnectedAt)
	assert.Equal(t, "", upstreams[1].Name)
	assert.Equal(t, "10.26.0.2", upstreams[1].Addr)

	// Other endpoints are unaffected.
	assert.Empty(t, m.Upstreams("other-endpoint"))

	m.RemoveConn(named)
	upstreams = m.Upstreams("my-endpoint")
	require.Len(t, upstreams, 1)
	assert.Equal(t, "10.26.0.2", upstreams[0].Addr)

	m.RemoveConn(unnamed)
	assert.Empty(t, m.Upstreams("my-endpoint"))
}

// Tests grouped upstreams are registered with the cluster once under their
// endpoint, with the group kept apart, and that removing an upstream twice
// only removes it once.
func TestLoadBalancedManager_ClusterGroups(t *testing.T) {
	state := cluster.NewState(&cluster.Node{ID: "local"}, log.NewNopLogger())
	m := NewLoadBalancedManager(state, nil)

	u1 := &fakeUpstream{endpointID: "my-endpoint", group: "branch-1"}
	u2 := &fakeUpstream{endpointID: "my-endpoint", group: "branch-1"}
	m.AddConn(u1)
	m.AddConn(u2)

	local := state.LocalNode()
	assert.Equal(t, map[string]int{"my-endpoint": 2}, local.Endpoints)
	assert.Equal(t, map[string]int{"my-endpoint/branch-1": 2}, local.Groups)
	assert.Equal(t, 2, local.Metadata().Upstreams)
	assert.Equal(t, 1, local.Metadata().Endpoints)
	assert.Equal(t, 2, state.AvgConns())

	// An upstream may be removed both when it refuses a connection and when
	// it disconnects, which must not remove the other upstreams.
	m.RemoveConn(u1)
	m.RemoveConn(u1)

	local = state.LocalNode()
	assert.Equal(t, map[string]int{"my-endpoint": 1}, local.Endpoints)
	assert.Equal(t, map[string]int{"my-endpoint/branch-1": 1}, local.Groups)

	u, ok := m.Select("my-endpoint", "branch-1", false)
	require.True(t, ok)
	assert.Equal(t, u2, u)

	m.RemoveConn(u2)
	local = state.LocalNode()
	assert.Empty(t, local.Endpoints)
	assert.Empty(t, local.Groups)
}
