package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// backendState is the runtime, mutable state of one backend: its current
// health and the probe counters backing it. All fields are guarded by mu.
type backendState struct {
	mu sync.Mutex

	// health is the current observed health. New connections are only routed to
	// healthy backends; a connect() failure still triggers per-request failover
	// regardless of this flag.
	health healthy

	// consecutive counters feed the threshold logic in the health checker.
	fails   int
	success int

	// lastErr and lastCheck aid operator debugging surfaced via /health.
	lastErr   string
	lastCheck time.Time
}

type healthy int

const (
	healthUnknown healthy = iota
	healthHealthy
	healthUnhealthy
)

// pool holds the backend addresses, their runtime states and the round-robin
// cursor. It is safe for concurrent use by the accept loop (cursor) and the
// health checker (states).
type pool struct {
	addresses []string
	states    []*backendState
	cursor    uint64
}

func newPool(addresses []string) *pool {
	states := make([]*backendState, len(addresses))
	for i := range states {
		states[i] = &backendState{health: healthUnknown}
	}
	return &pool{addresses: addresses, states: states}
}

// index returns the pool slot for a backend address, or -1 if unknown.
func (p *pool) index(addr string) int {
	for i, a := range p.addresses {
		if a == addr {
			return i
		}
	}
	return -1
}

// snapshot returns a copy of the backend health suitable for the status
// endpoint. It is read-locked per backend so a slow /health cannot block the
// probe loop.
func (p *pool) snapshot() []backendSnapshot {
	out := make([]backendSnapshot, len(p.states))
	for i, s := range p.states {
		s.mu.Lock()
		out[i] = backendSnapshot{
			Index:     i,
			Address:   p.addresses[i],
			Health:    s.health.String(),
			Fails:     s.fails,
			Success:   s.success,
			LastErr:   s.lastErr,
			LastCheck: s.lastCheck,
		}
		s.mu.Unlock()
	}
	return out
}

// nextHealthy advances the round-robin cursor and returns the index of the next
// healthy backend, or -1 when none is healthy. Backends are scanned starting at
// the cursor position so load spreads evenly across the healthy set.
func (p *pool) nextHealthy() int {
	n := len(p.states)
	if n == 0 {
		return -1
	}
	start := int(atomic.AddUint64(&p.cursor, 1)-1) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		p.states[idx].mu.Lock()
		h := p.states[idx].health
		p.states[idx].mu.Unlock()
		if h == healthHealthy {
			return idx
		}
	}
	return -1
}

// markResult records the outcome of a proxied connection attempt: success
// immediately restores health so a backend recovers without waiting for the next
// probe; failure leaves the probe loop to demote it.
func (p *pool) markResult(idx int, ok bool, err error) {
	s := p.states[idx]
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.health = healthHealthy
		s.success++
		s.fails = 0
		s.lastErr = ""
	} else {
		s.fails++
		if s.lastErr = errToString(err); s.fails >= 1 && s.health != healthUnhealthy {
			// Demote optimistically: a real connect failure is fresher than the
			// last probe. The next successful probe restores it.
			s.health = healthUnhealthy
		}
	}
}

func errToString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (h healthy) String() string {
	switch h {
	case healthHealthy:
		return "healthy"
	case healthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// backendSnapshot is the JSON-serializable view of one backend in /health.
type backendSnapshot struct {
	Index     int       `json:"index"`
	Address   string    `json:"address"`
	Health    string    `json:"health"`
	Fails     int       `json:"fails"`
	Success   int       `json:"success"`
	LastErr   string    `json:"lastErr,omitempty"`
	LastCheck time.Time `json:"lastCheck,omitempty"`
}
