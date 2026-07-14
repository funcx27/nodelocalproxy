package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
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

	stats *connectionStats
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
	p.stats.open()
	defer p.closeConn(client, "client")
	defer p.stats.close()
	addr := client.RemoteAddr().String()

	for attempt := 0; attempt < len(p.backends); attempt++ {
		idx := p.pool.nextHealthy()
		if idx < 0 {
			p.stats.fail()
			p.log.Warn("no healthy backend", "client", addr)
			return
		}
		b := p.backends[idx]

		d := net.Dialer{Timeout: p.dialTimeout}
		upstream, err := d.DialContext(ctx, "tcp", b)
		if err != nil {
			p.pool.markResult(idx, false, err)
			p.pool.markBackendConnectFailure(idx)
			p.log.Debug("backend connect failed, failing over", "backend", b, "client", addr, "err", err)
			continue
		}

		p.pool.markResult(idx, true, nil)
		p.pool.markBackendConnected(idx)
		p.stats.connect()
		p.log.Debug("connected", "backend", b, "client", addr)
		defer p.closeConn(upstream, "upstream")
		defer p.pool.markBackendClosed(idx)
		p.relay(client, upstream)
		return
	}
	p.stats.fail()
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

type connectionStats struct {
	active    atomic.Int64
	total     atomic.Uint64
	connected atomic.Uint64
	failed    atomic.Uint64
}

type connectionSnapshot struct {
	Active    int64  `json:"active"`
	Total     uint64 `json:"total"`
	Connected uint64 `json:"connected"`
	Failed    uint64 `json:"failed"`
}

func (s *connectionStats) open() {
	if s == nil {
		return
	}
	s.active.Add(1)
	s.total.Add(1)
}

func (s *connectionStats) close() {
	if s == nil {
		return
	}
	s.active.Add(-1)
}

func (s *connectionStats) connect() {
	if s == nil {
		return
	}
	s.connected.Add(1)
}

func (s *connectionStats) fail() {
	if s == nil {
		return
	}
	s.failed.Add(1)
}

func (s *connectionStats) snapshot() connectionSnapshot {
	if s == nil {
		return connectionSnapshot{}
	}
	return connectionSnapshot{
		Active:    s.active.Load(),
		Total:     s.total.Load(),
		Connected: s.connected.Load(),
		Failed:    s.failed.Load(),
	}
}
