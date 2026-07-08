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

type proxy struct {
	listen   string
	backends []string
	pool     *pool
	log      *slog.Logger

	// dialTimeout avoids waiting for the kernel TCP timeout before failover.
	dialTimeout time.Duration
}

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

func (p *proxy) handle(ctx context.Context, client net.Conn) {
	defer p.closeConn(client, "client")
	addr := client.RemoteAddr().String()

	for attempt := 0; attempt < len(p.backends); attempt++ {
		idx := p.pool.nextHealthy()
		if idx < 0 {
			p.log.Warn("no healthy backend", "client", addr)
			return
		}
		b := p.backends[idx]

		d := net.Dialer{Timeout: p.dialTimeout}
		upstream, err := d.DialContext(ctx, "tcp", b)
		if err != nil {
			p.pool.markResult(idx, false, err)
			p.log.Debug("backend connect failed, failing over", "backend", b, "client", addr, "err", err)
			continue
		}

		p.pool.markResult(idx, true, nil)
		p.log.Debug("connected", "backend", b, "client", addr)
		defer p.closeConn(upstream, "upstream")
		p.relay(client, upstream)
		return
	}
	p.log.Warn("all backends failed to connect", "client", addr)
}

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
	return errors.Is(err, net.ErrClosed)
}

// closeWrite falls back to Close for transports without TCP half-close support.
func closeWrite(c net.Conn) error {
	if tc, ok := c.(*net.TCPConn); ok {
		return tc.CloseWrite()
	}
	return c.Close()
}

func (p *proxy) closeConn(c net.Conn, side string) {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		p.log.Debug("connection close failed", "side", side, "err", err)
	}
}
