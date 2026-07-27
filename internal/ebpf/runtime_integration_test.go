//go:build ebpf && integration

package ebpf

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
)

// These tests attach cgroup/connect4 to the root cgroup for the test's
// duration. They require root, cgroup v2, BTF, and a kernel exposing the hook.
// Run with:
//
//	sudo go test -tags 'ebpf integration' -run TestIntegration -v ./internal/ebpf/
//
// Cold loopback ports (18080/18081) minimize blast radius: while attached, all
// connects from any process on the host to these backend addrs are rewritten.
// ebpf.Run is always cancelled / Closed at the end (detach).

// All loopback addresses (127.0.0.0/8) route to the local host. The BPF rewrite
// targets IP+port, so dialing 127.0.0.1:18080 and being rewritten to
// 127.0.0.2:18080 is observable by which echo server answers.
const (
	itBEA = "127.0.0.1:18080"
	itBEB = "127.0.0.2:18080"
)

// startEcho starts a TCP listener that echoes id on every accepted connection
// (write id, then half-close write side; client reads id then EOF). Returns
// the listener address. The goroutine stops when the listener is closed.
func startEcho(t *testing.T, addr, id string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte(id))
				_ = c.(*net.TCPConn).CloseWrite()
			}(c)
		}
	}()
	return ln
}

// dialEcho dials addr, reads the echo id, and returns it. A short read/write
// deadline guards against the connect being silently denied (BPF reject).
func dialEcho(t *testing.T, addr string) (string, error) {
	t.Helper()
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.Dial("tcp4", addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf, err := io.ReadAll(c)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func itPool(t *testing.T, healthyA, healthyB bool) (*backend.Pool, func(idx int, healthy bool)) {
	t.Helper()
	p := backend.NewPool([]string{itBEA, itBEB})
	set := func(idx int, healthy bool) {
		h := backend.HealthUnhealthy
		if healthy {
			h = backend.HealthHealthy
		}
		p.States[idx].Mu.Lock()
		p.States[idx].Health = h
		p.States[idx].Mu.Unlock()
	}
	set(0, healthyA)
	set(1, healthyB)
	return p, set
}

// startRuntime spins up ebpf.Run in a goroutine and blocks until the BPF link
// is attached (or ctx/Run returns early with an error). Returns a cancel
// function that also waits for Close to complete.
func startRuntime(t *testing.T, cfg *config.Config, pool *backend.Pool) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// Log to test output for visibility; Discard works too.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, pool, log) }()

	// Wait for attach: Status reports Attached once the link is up. Pre-attach
	// errors surface via errCh.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("ebpf.Run returned before attach: %v", err)
			}
			// Run returned nil — only happens after ctx cancel; treat as not
			// attached yet.
		default:
		}
		if Status().Attached {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !Status().Attached {
		cancel()
		t.Fatalf("ebpf runtime did not attach within timeout; status=%+v err=%v", Status(), <-errCh)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-errCh // Run returns after Close
		})
	}
}

func itConfig() *config.Config {
	return &config.Config{
		Mode:     "ebpf-transparent",
		Backends: []string{itBEA, itBEB},
	}
}

// TestIntegrationConnectRewritten validates that a connect to a backend addr is
// transparently rewritten to a healthy backend in the set. The disambiguating
// step marks backend A unhealthy and asserts the connect lands on B — proving
// the BPF actually matched on port+ip and rewrote, rather than passing the
// connect through unchanged (which would also echo A). If BPF does NOT rewrite
// here, the port byte-order is suspect (see htons in runtime.go).
func TestIntegrationConnectRewritten(t *testing.T) {
	// Sanity: ensure backends are reachable directly (no BPF in the path yet).
	lnA := startEcho(t, itBEA, "A")
	defer lnA.Close()
	lnB := startEcho(t, itBEB, "B")
	defer lnB.Close()

	// Disable self-exemption so the test process's own connects get rewritten.
	TestSetDisableSelfExempt(true)
	defer TestSetDisableSelfExempt(false)

	cfg := itConfig()
	pool, setHealth := itPool(t, true, true)
	stop := startRuntime(t, cfg, pool)
	defer stop()

	// 1. Both healthy: connect to A's addr must echo "A" or "B" (rewritten to a
	//    healthy backend in the set).
	got, err := dialEcho(t, itBEA)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	if got != "A" && got != "B" {
		t.Fatalf("dial A echoed %q, want A or B", got)
	}

	// 2. Disambiguation: mark A unhealthy, sync propagates within a second
	//    (syncLoop ticker), then dial A's addr again. With A out of the healthy
	//    set, BPF must rewrite to B → echo "B". A pass-through (no rewrite)
	//    would echo "A", proving the BPF did not match.
	setHealth(0, false)
	if err := waitForSync(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("map sync after marking A unhealthy: %v", err)
	}
	got, err = dialEcho(t, itBEA)
	if err != nil {
		t.Fatalf("dial A (A unhealthy): %v", err)
	}
	if got != "B" {
		t.Fatalf("dial A echoed %q with A unhealthy, want B (rewrite/rewrite-failover not working)", got)
	}

	// 3. Both unhealthy: BPF returns 0 → connect denied.
	setHealth(1, false)
	if err := waitForSync(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("map sync after marking B unhealthy: %v", err)
	}
	if _, err := dialEcho(t, itBEA); err == nil {
		t.Fatalf("dial A with all unhealthy: want connect denied, got nil error")
	} else {
		t.Logf("all-unhealthy connect denied as expected: %v", err)
	}
}

// TestIntegrationSelfExempt validates that with self-exemption ON (production
// default), a connect from the daemon's own cgroup is NOT rewritten — it lands
// directly on the dialed backend. This is the mechanism that keeps the daemon's
// health checker honest.
func TestIntegrationSelfExempt(t *testing.T) {
	lnA := startEcho(t, itBEA, "A")
	defer lnA.Close()
	lnB := startEcho(t, itBEB, "B")
	defer lnB.Close()

	TestSetDisableSelfExempt(false)
	defer TestSetDisableSelfExempt(false)

	cfg := itConfig()
	pool, _ := itPool(t, true, true)
	stop := startRuntime(t, cfg, pool)
	defer stop()

	got, err := dialEcho(t, itBEA)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	if got != "A" {
		t.Fatalf("self-exempt dial A echoed %q, want A (direct, not rewritten)", got)
	}
}

// TestIntegrationDetachOnExit validates that cancelling the runtime's context
// detaches the BPF link and closes the objects, so subsequent connects are
// direct (no rewriting, no deny).
func TestIntegrationDetachOnExit(t *testing.T) {
	lnA := startEcho(t, itBEA, "A")
	defer lnA.Close()

	TestSetDisableSelfExempt(true)
	defer TestSetDisableSelfExempt(false)

	cfg := itConfig()
	pool, _ := itPool(t, false, false) // all unhealthy → BPF would deny while attached

	stop := startRuntime(t, cfg, pool)
	stop() // cancel + wait for Close

	// After detach, the connect must reach A directly even though all backends
	// are unhealthy (the BPF deny no longer applies).
	got, err := dialEcho(t, itBEA)
	if err != nil {
		t.Fatalf("dial A after detach: %v", err)
	}
	if got != "A" {
		t.Fatalf("post-detach dial A echoed %q, want A (direct)", got)
	}

	// Status reflects detach.
	if st := Status(); st.Attached {
		t.Fatalf("status still attached after Close: %+v", st)
	}
}

// waitForSync blocks until syncOnce has run again (LastSync advanced past a
// reference zero) or deadline. The runtime syncLoop ticks every second, so 5s
// is ample headroom.
func waitForSync(deadline time.Time) error {
	prev := Status().LastSync
	for time.Now().Before(deadline) {
		cur := Status().LastSync
		if !cur.IsZero() && cur.After(prev) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timeout waiting for BPF map sync")
}
