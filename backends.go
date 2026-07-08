package main

import (
	"sync"
	"sync/atomic"
	"time"
)

type backendState struct {
	mu sync.Mutex

	health healthy

	fails   int
	success int

	lastErr   string
	lastCheck time.Time
}

type healthy int

const (
	healthUnknown healthy = iota
	healthHealthy
	healthUnhealthy
)

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

func (p *pool) index(addr string) int {
	for i, a := range p.addresses {
		if a == addr {
			return i
		}
	}
	return -1
}

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

func (p *pool) markResult(idx int, ok bool, err error) {
	s := p.states[idx]
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.health = healthHealthy
		s.success++
		s.fails = 0
		s.lastErr = ""
		return
	}

	s.fails++
	s.lastErr = errToString(err)
	if s.health != healthUnhealthy {
		s.health = healthUnhealthy
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

type backendSnapshot struct {
	Index     int       `json:"index"`
	Address   string    `json:"address"`
	Health    string    `json:"health"`
	Fails     int       `json:"fails"`
	Success   int       `json:"success"`
	LastErr   string    `json:"lastErr,omitempty"`
	LastCheck time.Time `json:"lastCheck,omitempty"`
}
