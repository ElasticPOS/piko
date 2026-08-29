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
}

func (u *fakeUpstream) EndpointID() string {
	return u.endpointID
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

	assert.False(t, lb.Remove(u2))
	assert.False(t, lb.Remove(u3))
	assert.Equal(t, "1", lb.Next().EndpointID())
	assert.Equal(t, "4", lb.Next().EndpointID())
	assert.Equal(t, "1", lb.Next().EndpointID())

	assert.False(t, lb.Remove(u1))
	assert.True(t, lb.Remove(u4))

	assert.Nil(t, lb.Next())
}

// Tests the manager describes the clients connected to the local node for an
// endpoint.
func TestLoadBalancedManager_Upstreams(t *testing.T) {
	m := NewLoadBalancedManager(
		cluster.NewState(&cluster.Node{ID: "local"}, log.NewNopLogger()),
		nil,
	)

	assert.Empty(t, m.Upstreams("my-endpoint"))

	named := NewConnUpstream("my-endpoint", nil, "my-host", "10.26.0.1")
	// Clients aren't required to send a name.
	unnamed := NewConnUpstream("my-endpoint", nil, "", "10.26.0.2")
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
