package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/funcx27/nodelocalproxy/internal/backend"
)

type Proxy struct {
	listen   string
	backends []string
	pool     *backend.Pool
	log      *slog.Logger

	// dialTimeout avoids waiting for the kernel TCP timeout before failover.
	dialTimeout time.Duration

	stats *ConnectionStats
}

// NewProxy constructs a Proxy with the supplied dependencies.
func NewProxy(listen string, backends []string, pool *backend.Pool, log *slog.Logger, dialTimeout time.Duration, stats *ConnectionStats) *Proxy {
	return &Proxy{
		listen:      listen,
		backends:    backends,
		pool:        pool,
		log:         log,
		dialTimeout: dialTimeout,
		stats:       stats,
	}
}

func (p *Proxy) Serve(ctx context.Context, ln net.Listener) error {
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

func (p *Proxy) handle(ctx context.Context, client net.Conn) {
	p.stats.Open()
	defer p.closeConn(client, "client")
	defer p.stats.Close()
	addr := client.RemoteAddr().String()

	for attempt := 0; attempt < len(p.backends); attempt++ {
		idx := p.pool.NextHealthy()
		if idx < 0 {
			p.stats.Fail()
			p.log.Warn("no healthy backend", "client", addr)
			return
		}
		b := p.backends[idx]

		d := net.Dialer{Timeout: p.dialTimeout}
		upstream, err := d.DialContext(ctx, "tcp", b)
		if err != nil {
			p.pool.MarkResult(idx, false, err)
			p.pool.MarkBackendConnectFailure(idx)
			p.log.Debug("backend connect failed, failing over", "backend", b, "client", addr, "err", err)
			continue
		}

		p.pool.MarkResult(idx, true, nil)
		p.pool.MarkBackendConnected(idx)
		p.stats.Connect()
		p.log.Debug("connected", "backend", b, "client", addr)
		defer p.closeConn(upstream, "upstream")
		defer p.pool.MarkBackendClosed(idx)
		p.relay(client, upstream)
		return
	}
	p.stats.Fail()
	p.log.Warn("all backends failed to connect", "client", addr)
}

func (p *Proxy) relay(a, b net.Conn) {
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

func (p *Proxy) closeConn(c net.Conn, side string) {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		p.log.Debug("connection close failed", "side", side, "err", err)
	}
}

// ConnectionStats tracks proxy connection counters.
type ConnectionStats struct {
	active    atomic.Int64
	total     atomic.Uint64
	connected atomic.Uint64
	failed    atomic.Uint64
}

// ConnectionSnapshot is the read-only view of ConnectionStats.
type ConnectionSnapshot struct {
	Active    int64  `json:"active"`
	Total     uint64 `json:"total"`
	Connected uint64 `json:"connected"`
	Failed    uint64 `json:"failed"`
}

func (s *ConnectionStats) Open() {
	if s == nil {
		return
	}
	s.active.Add(1)
	s.total.Add(1)
}

func (s *ConnectionStats) Close() {
	if s == nil {
		return
	}
	s.active.Add(-1)
}

func (s *ConnectionStats) Connect() {
	if s == nil {
		return
	}
	s.connected.Add(1)
}

func (s *ConnectionStats) Fail() {
	if s == nil {
		return
	}
	s.failed.Add(1)
}

func (s *ConnectionStats) Snapshot() ConnectionSnapshot {
	if s == nil {
		return ConnectionSnapshot{}
	}
	return ConnectionSnapshot{
		Active:    s.active.Load(),
		Total:     s.total.Load(),
		Connected: s.connected.Load(),
		Failed:    s.failed.Load(),
	}
}
