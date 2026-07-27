package backend

import (
	"sync"
	"sync/atomic"
	"time"
)

type backendState struct {
	Mu sync.Mutex

	Health Healthy

	Fails   int
	Success int

	LastErr     string
	LastCheck   time.Time
	LastSuccess time.Time

	ActiveConnections atomic.Int64
	TotalConnections  atomic.Uint64
	FailedConnections atomic.Uint64
}

// Healthy represents the health classification of a backend.
type Healthy int

const (
	HealthUnknown Healthy = iota
	HealthHealthy
	HealthUnhealthy
)

// Pool is a round-robin pool of health-checked backends.
type Pool struct {
	addresses []string
	States    []*backendState
	cursor    uint64
}

func NewPool(addresses []string) *Pool {
	states := make([]*backendState, len(addresses))
	for i := range states {
		states[i] = &backendState{Health: HealthUnknown}
	}
	return &Pool{addresses: addresses, States: states}
}

func (p *Pool) index(addr string) int {
	for i, a := range p.addresses {
		if a == addr {
			return i
		}
	}
	return -1
}

func (p *Pool) Snapshot() []BackendSnapshot {
	out := make([]BackendSnapshot, len(p.States))
	for i, s := range p.States {
		s.Mu.Lock()
		out[i] = BackendSnapshot{
			Index:       i,
			Address:     p.addresses[i],
			Health:      s.Health.String(),
			LastErr:     s.LastErr,
			LastCheck:   s.LastCheck,
			LastSuccess: s.LastSuccess,
			Connections: BackendConnectionSnapshot{
				Active: s.ActiveConnections.Load(),
				Total:  s.TotalConnections.Load(),
				Failed: s.FailedConnections.Load(),
			},
		}
		s.Mu.Unlock()
	}
	return out
}

func (p *Pool) MarkBackendConnected(idx int) {
	s, ok := p.state(idx)
	if !ok {
		return
	}
	s.ActiveConnections.Add(1)
	s.TotalConnections.Add(1)
}

func (p *Pool) MarkBackendClosed(idx int) {
	s, ok := p.state(idx)
	if !ok {
		return
	}
	s.ActiveConnections.Add(-1)
}

func (p *Pool) MarkBackendConnectFailure(idx int) {
	s, ok := p.state(idx)
	if !ok {
		return
	}
	s.FailedConnections.Add(1)
}

func (p *Pool) state(idx int) (*backendState, bool) {
	if idx < 0 || idx >= len(p.States) {
		return nil, false
	}
	return p.States[idx], true
}

func (p *Pool) NextHealthy() int {
	n := len(p.States)
	if n == 0 {
		return -1
	}
	start := int(atomic.AddUint64(&p.cursor, 1)-1) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		p.States[idx].Mu.Lock()
		h := p.States[idx].Health
		p.States[idx].Mu.Unlock()
		if h == HealthHealthy {
			return idx
		}
	}
	return -1
}

func (p *Pool) MarkResult(idx int, ok bool, err error) {
	s := p.States[idx]
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if ok {
		s.Health = HealthHealthy
		s.Success++
		s.Fails = 0
		s.LastSuccess = time.Now()
		s.LastErr = ""
		return
	}

	s.Fails++
	s.LastErr = errToString(err)
	if s.Health != HealthUnhealthy {
		s.Health = HealthUnhealthy
	}
}

func errToString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (h Healthy) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// BackendSnapshot is the per-backend view exposed in status responses.
type BackendSnapshot struct {
	Index       int                       `json:"index"`
	Address     string                    `json:"address"`
	Health      string                    `json:"health"`
	LastErr     string                    `json:"lastErr,omitempty"`
	LastCheck   time.Time                 `json:"lastCheck,omitempty"`
	LastSuccess time.Time                 `json:"lastSuccess,omitempty"`
	Connections BackendConnectionSnapshot `json:"connections"`
}

// BackendConnectionSnapshot is the connection-count view for a single backend.
type BackendConnectionSnapshot struct {
	Active int64  `json:"active"`
	Total  uint64 `json:"total"`
	Failed uint64 `json:"failed"`
}
