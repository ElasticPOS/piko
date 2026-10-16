package yamuxconn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/andydunstall/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordConn records each write as a separate chunk.
type recordConn struct {
	net.Conn
	writes [][]byte
}

func (c *recordConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), b...))
	return len(b), nil
}

func header(typ uint8, length uint32) []byte {
	b := make([]byte, headerSize)
	b[1] = typ
	binary.BigEndian.PutUint32(b[8:12], length)
	return b
}

func TestConn_CoalescesDataFrame(t *testing.T) {
	rec := &recordConn{}
	conn := New(rec)

	hdr := header(typeData, 3)
	n, err := conn.Write(hdr)
	require.NoError(t, err)
	assert.Equal(t, headerSize, n)
	assert.Empty(t, rec.writes)

	n, err = conn.Write([]byte("foo"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	require.Len(t, rec.writes, 1)
	assert.Equal(t, append(hdr, []byte("foo")...), rec.writes[0])
}

func TestConn_PassesThroughFramesWithoutBody(t *testing.T) {
	rec := &recordConn{}
	conn := New(rec)

	// Window update, ping and a data frame with no body (such as a FIN) have
	// nothing following them, so must be written immediately.
	frames := [][]byte{
		header(1, 1024),
		header(2, 7),
		header(typeData, 0),
	}
	for _, f := range frames {
		_, err := conn.Write(f)
		require.NoError(t, err)
	}
	assert.Equal(t, frames, rec.writes)
}

// TestConn_Yamux checks a yamux session works end to end over the wrapper,
// with each data frame arriving as a single write.
func TestConn_Yamux(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	client, err := yamux.Client(New(clientConn), nil)
	require.NoError(t, err)
	defer client.Close()
	server, err := yamux.Server(New(serverConn), nil)
	require.NoError(t, err)
	defer server.Close()

	payload := make([]byte, 1024*1024)
	for i := range payload {
		payload[i] = byte(i)
	}

	go func() {
		stream, err := client.Open()
		if err != nil {
			return
		}
		_, _ = stream.Write(payload)
		_ = stream.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := server.AcceptStreamWithContext(ctx)
	require.NoError(t, err)

	b, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, payload, b)
}
