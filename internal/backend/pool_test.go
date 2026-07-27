package backend

import (
	"sync"
	"testing"
)

func TestPoolNextHealthyRoundRobin(t *testing.T) {
	p := NewPool([]string{"a:1", "b:1", "c:1"})
	for i, h := range []Healthy{HealthHealthy, HealthUnhealthy, HealthHealthy} {
		setBackendHealth(p, i, h)
	}

	got := map[int]bool{}
	for i := 0; i < 6; i++ {
		idx := p.NextHealthy()
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
	p := NewPool([]string{"a:1", "b:1", "c:1"})
	if idx := p.index("b:1"); idx != 1 {
		t.Fatalf("index(b:1): got %d want 1", idx)
	}
	if idx := p.index("missing:1"); idx != -1 {
		t.Fatalf("index(missing): got %d want -1", idx)
	}
}

func TestPoolNextHealthyNoneHealthy(t *testing.T) {
	p := NewPool([]string{"a:1", "b:1"})
	for i := range p.States {
		setBackendHealth(p, i, HealthUnhealthy)
	}
	if idx := p.NextHealthy(); idx != -1 {
		t.Fatalf("expected -1 when no backend healthy, got %d", idx)
	}
}

func TestPoolMarkResultRestoresHealth(t *testing.T) {
	p := NewPool([]string{"a:1"})
	s := p.States[0]
	s.Mu.Lock()
	s.Health = HealthUnhealthy
	s.Fails = 5
	s.Mu.Unlock()

	p.MarkResult(0, true, nil)
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.Health != HealthHealthy {
		t.Fatalf("expected healthy after success, got %s", s.Health)
	}
	if s.Fails != 0 {
		t.Fatalf("expected fails reset to 0, got %d", s.Fails)
	}
}

func TestPoolSnapshotHasAddress(t *testing.T) {
	p := NewPool([]string{"a:1", "b:1"})
	snap := p.Snapshot()
	if len(snap) != 2 || snap[0].Address != "a:1" || snap[1].Address != "b:1" {
		t.Fatalf("snapshot addresses: %+v", snap)
	}
}

func TestPoolSnapshotConcurrent(t *testing.T) {
	p := NewPool([]string{"a:1", "b:1", "c:1", "d:1", "e:1"})
	for i := range p.States {
		setBackendHealth(p, i, HealthHealthy)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = p.Snapshot()
		}()
		go func() {
			defer wg.Done()
			p.MarkResult(0, true, nil)
		}()
	}
	wg.Wait()
}

func setBackendHealth(p *Pool, idx int, h Healthy) {
	s := p.States[idx]
	s.Mu.Lock()
	s.Health = h
	s.Mu.Unlock()
}
