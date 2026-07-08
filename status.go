package main

import (
	"encoding/json"
	"net"
	"net/http"
	"time"
)

// statusServer exposes the proxy state on a localhost-only HTTP endpoint so an
// operator can inspect backend health (curl <status>/health) without parsing
// journald. It binds to the Status address from config, which should be
// 127.0.0.1 to avoid exposing cluster internals to the network.
type statusServer struct {
	listen   string
	backends []Backend
	pool     *pool
	started  time.Time
}

func (s *statusServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	return mux
}

// handleHealth returns the per-backend health snapshot and an overall field
// derived from whether any backend is currently healthy.
func (s *statusServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	backends := s.pool.snapshot()
	anyHealthy := false
	for _, b := range backends {
		if b.Health == healthHealthy.String() {
			anyHealthy = true
			break
		}
	}
	resp := struct {
		Status       string            `json:"status"`
		Listen       string            `json:"listen"`
		Uptime       float64           `json:"uptimeSeconds"`
		BackendAddrs []string          `json:"backendAddresses"`
		Backends     []backendSnapshot `json:"backends"`
	}{
		Status:   healthOverall(anyHealthy),
		Listen:   s.listen,
		Uptime:   time.Since(s.started).Seconds(),
		Backends: backends,
	}
	for _, b := range s.backends {
		resp.BackendAddrs = append(resp.BackendAddrs, b.Address)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func healthOverall(anyHealthy bool) string {
	if anyHealthy {
		return "ok"
	}
	return "degraded"
}

func (s *statusServer) serve(ln net.Listener) error {
	srv := &http.Server{Handler: s.routes()}
	return srv.Serve(ln)
}
