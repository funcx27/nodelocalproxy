package main

import (
	"sync"
	"testing"
)

func TestPoolNextHealthyRoundRobin(t *testing.T) {
	p := newPool([]string{"a:1", "b:1", "c:1"})
	// mark backends 0 and 2 healthy, 1 unhealthy
	for i, healthy := range []bool{true, false, true} {
		s := p.states[i]
		s.mu.Lock()
		if healthy {
			s.health = healthHealthy
		} else {
			s.health = healthUnhealthy
		}
		s.mu.Unlock()
	}

	// Round-robin should alternate between 0 and 2, skipping 1.
	got := map[int]bool{}
	for i := 0; i < 6; i++ {
		idx := p.nextHealthy()
		if idx < 0 {
			t.Fatal("expected a healthy backend")
		}
		got[idx] = true
	}
	if !got[0] || !got[2] {
		t.Fatalf("round-robin missed a healthy backend: %v", got)
	}
	if got[1] {
		t.Fatal("unhealthy backend was selected")
	}
}

func TestPoolIndexByAddress(t *testing.T) {
	p := newPool([]string{"a:1", "b:1", "c:1"})
	if idx := p.index("b:1"); idx != 1 {
		t.Fatalf("index(b:1): got %d want 1", idx)
	}
	if idx := p.index("missing:1"); idx != -1 {
		t.Fatalf("index(missing): got %d want -1", idx)
	}
}

func TestPoolNextHealthyNoneHealthy(t *testing.T) {
	p := newPool([]string{"a:1", "b:1"})
	for _, s := range p.states {
		s.mu.Lock()
		s.health = healthUnhealthy
		s.mu.Unlock()
	}
	if idx := p.nextHealthy(); idx != -1 {
		t.Fatalf("expected -1 when no backend healthy, got %d", idx)
	}
}

func TestPoolMarkResultRestoresHealth(t *testing.T) {
	p := newPool([]string{"a:1"})
	s := p.states[0]
	s.mu.Lock()
	s.health = healthUnhealthy
	s.fails = 5
	s.mu.Unlock()

	// A successful proxied connection should immediately restore health.
	p.markResult(0, true, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health != healthHealthy {
		t.Fatalf("expected healthy after success, got %s", s.health)
	}
	if s.fails != 0 {
		t.Fatalf("expected fails reset to 0, got %d", s.fails)
	}
}

func TestPoolSnapshotHasAddress(t *testing.T) {
	p := newPool([]string{"a:1", "b:1"})
	snap := p.snapshot()
	if len(snap) != 2 || snap[0].Address != "a:1" || snap[1].Address != "b:1" {
		t.Fatalf("snapshot addresses: %+v", snap)
	}
}

func TestPoolSnapshotConcurrent(t *testing.T) {
	p := newPool([]string{"a:1", "b:1", "c:1", "d:1", "e:1"})
	for _, s := range p.states {
		s.mu.Lock()
		s.health = healthHealthy
		s.mu.Unlock()
	}

	// Concurrent snapshots + markResult should not race.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = p.snapshot() }()
		go func() { defer wg.Done(); p.markResult(0, true, nil) }()
	}
	wg.Wait()
}
