package client

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Forwarder manages forwarding incoming connections to another address.
type Forwarder struct {
	// ctx is a context to close the forwarder when canceled.
	ctx context.Context

	// ln is the listener to accept connections on.
	ln *listener

	// addr is the address to forward connections to.
	addr string

	group *errgroup.Group

	logger Logger
}

func newForwarder(ctx context.Context, ln *listener, addr string, logger Logger) *Forwarder {
	group, ctx := errgroup.WithContext(ctx)
	f := &Forwarder{
		ctx:    ctx,
		ln:     ln,
		addr:   addr,
		group:  group,
		logger: logger,
	}

	f.group.Go(func() error {
		return f.accept()
	})

	return f
}

// Wait blocks until the forwarder exits, either due to an error or being
// closed.
func (f *Forwarder) Wait() error {
	err := f.group.Wait()
	if f.ctx.Err() != nil || errors.Is(err, ErrClosed) {
		// Ignore context canceled and shutdown errors as they indicate
		// a graceful shutdown.
		return nil
	}
	return err
}

// Close stops the forwarder and closes the underlying connection to the Piko
// server, tearing down all connections multiplexed over it.
func (f *Forwarder) Close() error {
	// Shutdown (not Close): Close only sends GoAway, leaving the underlying
	// yamux session open with nothing left to close it — a leak. Shutdown
	// closes the session and its connection.
	return f.ln.Shutdown()
}

func (f *Forwarder) accept() error {
	// Shutdown (not Close) so the underlying yamux session/connection is
	// actually released when the accept loop exits; Close only sends GoAway.
	defer f.ln.Shutdown()

	for {
		conn, err := f.ln.AcceptWithContext(f.ctx)
		if err != nil {
			return err
		}

		f.group.Go(func() error {
			f.forward(conn)
			return nil
		})
	}
}

func (f *Forwarder) forward(downstream net.Conn) {
	defer downstream.Close()

	dialer := &net.Dialer{}
	upstream, err := dialer.DialContext(f.ctx, "tcp", f.addr)
	if err != nil {
		if f.ctx.Err() != nil {
			// If the context was canceled don't log an error.
			return
		}
		f.logger.Warn(
			"failed to dial upstream",
			zap.String("addr", f.addr),
			zap.Error(err),
		)
		return
	}

	g := &sync.WaitGroup{}
	g.Add(2)

	go func() {
		defer g.Done()
		defer downstream.Close()
		_, err := io.Copy(downstream, upstream)
		if err != nil {
			f.logger.Debug(
				"copy to downstream closed",
				zap.String("addr", f.addr),
				zap.Error(err),
			)
		}
	}()

	go func() {
		defer g.Done()
		defer upstream.Close()
		_, err := io.Copy(upstream, downstream)
		if err != nil {
			f.logger.Debug(
				"copy to upstream closed",
				zap.String("addr", f.addr),
				zap.Error(err),
			)
		}
	}()

	g.Wait()
}
