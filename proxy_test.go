package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// quietLogger returns a logger that discards output during tests.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startEcho starts a TCP server that echoes received bytes back to the client.
// It returns the listener (so callers can read Addr) and a stop function.
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
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln, func() {
		_ = ln.Close()
		wg.Wait()
	}
}

// TestProxyFailover sets up two backends; the first is closed so the dial fails,
// the second is a healthy echo server. The proxy must fail over and the client
// receives its data echoed back.
func TestProxyFailover(t *testing.T) {
	// Backend 0: closed port → dial fails immediately.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	deadAddr := deadLn.Addr().String()
	_ = deadLn.Close()

	// Backend 1: healthy echo server.
	echoLn, stopEcho := startEcho(t)
	defer stopEcho()
	echoAddr := echoLn.Addr().String()

	// Proxy: listen on an ephemeral port, pool with both backends marked healthy.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	defer ln.Close()

	proxyLn := &proxy{
		listen:      ln.Addr().String(),
		dialTimeout: 500 * time.Millisecond,
		backends: []Backend{
			{Address: deadAddr, HealthCheck: HealthCheck{Type: "tcp"}},
			{Address: echoAddr, HealthCheck: HealthCheck{Type: "tcp"}},
		},
		pool: newPool(2),
		log:  quietLogger(),
	}
	// Both backends start healthy so the proxy will try backend 0, fail, and
	// fail over to backend 1.
	for _, s := range proxyLn.pool.states {
		s.mu.Lock()
		s.health = healthHealthy
		s.mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go proxyLn.serve(ctx, ln)

	// Client: send and expect echo.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
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
}

// TestProxyAllDown verifies that when every backend is unreachable the client
// connection is closed (no healthy backend) rather than hanging.
func TestProxyAllDown(t *testing.T) {
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	deadAddr := deadLn.Addr().String()
	_ = deadLn.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	defer ln.Close()

	pr := &proxy{
		listen:      ln.Addr().String(),
		dialTimeout: 300 * time.Millisecond,
		backends:    []Backend{{Address: deadAddr, HealthCheck: HealthCheck{Type: "tcp"}}},
		pool:        newPool(1),
		log:         quietLogger(),
	}
	pr.pool.states[0].mu.Lock()
	pr.pool.states[0].health = healthHealthy
	pr.pool.states[0].mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pr.serve(ctx, ln)

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	// The proxy should close the connection promptly since it cannot connect.
	done := make(chan struct{})
	go func() {
		_, _ = conn.Read(make([]byte, 16))
		close(done)
	}()
	select {
	case <-done:
		// connection closed by proxy — expected.
	case <-time.After(3 * time.Second):
		t.Fatal("proxy hung instead of closing the client connection")
	}
}

// TestHealthCheckHTTP verifies the HTTP probe marks a 2xx backend healthy and a
// 5xx one unhealthy after reaching the failure threshold.
func TestHealthCheckHTTP(t *testing.T) {
	// Healthy upstream: 200.
	okSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	// Unhealthy upstream: 503.
	badSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badSrv.Close()

	backends := []Backend{
		{Address: stripHost(okSrv.Listener.Addr().String()), HealthCheck: HealthCheck{Type: "http", Path: "/readyz", InsecureSkipVerify: true, Interval: 50 * time.Millisecond, Timeout: time.Second, FailureThreshold: 1, SuccessThreshold: 1}},
		{Address: stripHost(badSrv.Listener.Addr().String()), HealthCheck: HealthCheck{Type: "http", Path: "/readyz", InsecureSkipVerify: true, Interval: 50 * time.Millisecond, Timeout: time.Second, FailureThreshold: 1, SuccessThreshold: 1}},
	}
	p := newPool(2)
	log := quietLogger()
	ch := newChecker(p, backends, log)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go ch.run(ctx)

	// Wait for probes to settle.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		p.states[0].mu.Lock()
		h0 := p.states[0].health
		p.states[0].mu.Unlock()
		p.states[1].mu.Lock()
		h1 := p.states[1].health
		p.states[1].mu.Unlock()
		if h0 == healthHealthy && h1 == healthUnhealthy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("health states did not converge: see logs")
}

// stripHost maps the httptest IPv6 listener address back to IPv4 loopback, since
// the backends in tests bind 127.0.0.1. host:port strings are returned as-is.
func stripHost(addr string) string {
	if strings.HasPrefix(addr, "[::]:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	return addr
}
