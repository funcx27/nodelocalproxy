package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"log/slog"
)

// proxy accepts connections on the listen address and forwards each one to a
// healthy backend. Backend selection is round-robin over the healthy set; a
// connect() failure triggers per-request failover to the next healthy backend,
// so a backend can fail and recover within a single health-check interval
// without dropping the request.
type proxy struct {
	listen   string
	backends []Backend
	pool     *pool
	log      *slog.Logger

	// dialTimeout bounds the connect attempt to each backend. If the upstream is
	// down the proxy must move on quickly rather than hold the client connection
	// open until the kernel TCP timeout.
	dialTimeout time.Duration
}

const defaultDialTimeout = 2 * time.Second

// errNoHealthyBackend means every backend is currently unhealthy. The connection
// is rejected so the client sees a clear failure rather than an indefinite hang.
var errNoHealthyBackend = errors.New("no healthy backend available")

// serve accepts and handles connections until the listener is closed. It blocks
// for the lifetime of the proxy.
func (p *proxy) serve(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if isClosed(err) {
				return nil
			}
			return err
		}
		go p.handle(ctx, conn)
	}
}

// handle forwards one client connection. It selects backends in round-robin
// order, skipping any that fail to connect, so a down backend is tolerated
// without surfacing an error to the client as long as one backend connects.
func (p *proxy) handle(ctx context.Context, client net.Conn) {
	defer client.Close()
	addr := client.RemoteAddr().String()

	for attempt := 0; attempt < len(p.backends); attempt++ {
		idx := p.pool.nextHealthy()
		if idx < 0 {
			p.log.Warn("no healthy backend", "client", addr)
			return
		}
		b := p.backends[idx]

		d := net.Dialer{Timeout: p.dialTimeout}
		upstream, err := d.DialContext(ctx, "tcp", b.Address)
		if err != nil {
			p.pool.markResult(idx, false, err)
			p.log.Debug("backend connect failed, failing over", "backend", b.Address, "client", addr, "err", err)
			continue
		}

		p.pool.markResult(idx, true, nil)
		p.log.Debug("connected", "backend", b.Address, "client", addr)
		p.relay(client, upstream)
		return
	}
	p.log.Warn("all backends failed to connect", "client", addr)
}

// relay copies bytes bidirectionally and waits for both directions to finish
// before returning. The connection is half-closed cleanly when either side
// finishes sending; both goroutines closing is what allows handle to return.
func (p *proxy) relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = closeWrite(a)
	}()
	wg.Wait()
}

func isClosed(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return false
	}
	return errors.Is(err, net.ErrClosed)
}

// closeWrite signals EOF to the peer on the write half. TCPConn implements it;
// we fall back to a full Close for non-TCP transports where half-close is not
// supported, which still terminates the relay correctly.
func closeWrite(c net.Conn) error {
	if tc, ok := c.(*net.TCPConn); ok {
		return tc.CloseWrite()
	}
	return c.Close()
}
