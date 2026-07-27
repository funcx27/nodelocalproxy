package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startEcho(t *testing.T) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer closeTestConn(t, c, "echo client")
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln, func() {
		closeTestConn(t, ln, "echo listener")
		wg.Wait()
	}
}

func markAllHealthy(p *backend.Pool) {
	for _, s := range p.States {
		s.Mu.Lock()
		s.Health = backend.HealthHealthy
		s.Mu.Unlock()
	}
}

func startProxy(t *testing.T, p *Proxy, ln net.Listener) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- p.Serve(ctx, ln)
	}()

	t.Cleanup(func() {
		cancel()
		closeTestConn(t, ln, "proxy listener")
		if err := <-serveErr; err != nil {
			t.Errorf("proxy serve: %v", err)
		}
	})
}

func TestProxyFailover(t *testing.T) {
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	deadAddr := deadLn.Addr().String()
	closeTestConn(t, deadLn, "dead backend")

	echoLn, stopEcho := startEcho(t)
	defer stopEcho()
	echoAddr := echoLn.Addr().String()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}

	p := &Proxy{
		listen:      ln.Addr().String(),
		dialTimeout: 500 * time.Millisecond,
		backends:    []string{deadAddr, echoAddr},
		pool:        backend.NewPool([]string{deadAddr, echoAddr}),
		log:         quietLogger(),
		stats:       &ConnectionStats{},
	}
	markAllHealthy(p.pool)
	startProxy(t, p, ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer closeTestConn(t, conn, "proxy client")
	payload := "hello-failover"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("echo mismatch: got %q", buf)
	}
	stats := p.stats.Snapshot()
	if stats.Total != 1 {
		t.Fatalf("stats total: got %d want 1", stats.Total)
	}
	if stats.Connected != 1 {
		t.Fatalf("stats connected: got %d want 1", stats.Connected)
	}
	if stats.Failed != 0 {
		t.Fatalf("stats failed: got %d want 0", stats.Failed)
	}
	backends := p.pool.Snapshot()
	if backends[0].Connections.Failed != 1 {
		t.Fatalf("dead backend failed connections: got %d want 1", backends[0].Connections.Failed)
	}
	if backends[1].Connections.Total != 1 {
		t.Fatalf("echo backend total connections: got %d want 1", backends[1].Connections.Total)
	}
}

func TestProxyAllDown(t *testing.T) {
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	deadAddr := deadLn.Addr().String()
	closeTestConn(t, deadLn, "dead backend")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}

	p := &Proxy{
		listen:      ln.Addr().String(),
		dialTimeout: 300 * time.Millisecond,
		backends:    []string{deadAddr},
		pool:        backend.NewPool([]string{deadAddr}),
		log:         quietLogger(),
		stats:       &ConnectionStats{},
	}
	markAllHealthy(p.pool)
	startProxy(t, p, ln)

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer closeTestConn(t, conn, "proxy client")

	done := make(chan struct{})
	go func() {
		_, _ = conn.Read(make([]byte, 16))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy hung instead of closing the client connection")
	}
	stats := p.stats.Snapshot()
	if stats.Total != 1 {
		t.Fatalf("stats total: got %d want 1", stats.Total)
	}
	if stats.Connected != 0 {
		t.Fatalf("stats connected: got %d want 0", stats.Connected)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats failed: got %d want 1", stats.Failed)
	}
	backends := p.pool.Snapshot()
	if backends[0].Connections.Failed != 1 {
		t.Fatalf("backend failed connections: got %d want 1", backends[0].Connections.Failed)
	}
}

func TestHealthCheckHTTP(t *testing.T) {
	okSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	badSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badSrv.Close()

	okAddr := stripHost(okSrv.Listener.Addr().String())
	badAddr := stripHost(badSrv.Listener.Addr().String())
	hc := config.HealthCheck{
		Type:               "http",
		Path:               "/readyz",
		InsecureSkipVerify: true,
		Interval:           50 * time.Millisecond,
		Timeout:            time.Second,
		FailureThreshold:   1,
		SuccessThreshold:   1,
	}
	p := backend.NewPool([]string{okAddr, badAddr})
	ch := backend.NewChecker(p, []string{okAddr, badAddr}, hc, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go ch.Run(ctx)

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		p.States[0].Mu.Lock()
		h0 := p.States[0].Health
		p.States[0].Mu.Unlock()
		p.States[1].Mu.Lock()
		h1 := p.States[1].Health
		p.States[1].Mu.Unlock()
		if h0 == backend.HealthHealthy && h1 == backend.HealthUnhealthy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("health states did not converge: see logs")
}

func stripHost(addr string) string {
	if strings.HasPrefix(addr, "[::]:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	return addr
}

func closeTestConn(t *testing.T, c io.Closer, name string) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("%s close: %v", name, err)
	}
}
