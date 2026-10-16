// Package yamuxconn wraps the connection under a yamux session so each data
// frame is written as a single write.
//
// Yamux writes every frame as two writes: the 12 byte header, then the body.
// Piko runs yamux over a WebSocket, where each write becomes its own message,
// so every data frame was sent as a tiny header message followed by the body.
// Over a slow uplink those extra messages (each its own TLS record and
// usually its own packet) cost a significant share of the throughput.
package yamuxconn

import (
	"encoding/binary"
	"net"
)

const (
	headerSize = 12

	// typeData is the yamux frame type of a data frame, which is the only
	// frame type followed by a body.
	typeData = 0
)

// Conn holds back the header of a data frame and sends it together with the
// frame body in a single write.
//
// Yamux only ever writes from its send loop, so writes are never concurrent,
// and the bytes on the wire are unchanged, only how they're split into
// writes.
type Conn struct {
	net.Conn

	// pending is a data frame header waiting for its body.
	pending []byte
	buf     []byte
}

func New(conn net.Conn) *Conn {
	return &Conn{
		Conn:    conn,
		pending: make([]byte, 0, headerSize),
	}
}

func (c *Conn) Write(b []byte) (int, error) {
	if len(c.pending) == 0 {
		if isDataHeader(b) {
			c.pending = append(c.pending, b...)
			return len(b), nil
		}
		return c.Conn.Write(b)
	}

	c.buf = append(append(c.buf[:0], c.pending...), b...)
	c.pending = c.pending[:0]
	if _, err := c.Conn.Write(c.buf); err != nil {
		return 0, err
	}
	return len(b), nil
}

// isDataHeader returns whether b is exactly the header of a data frame with
// a body.
func isDataHeader(b []byte) bool {
	return len(b) == headerSize &&
		b[1] == typeData &&
		binary.BigEndian.Uint32(b[8:12]) > 0
}

var _ net.Conn = &Conn{}
