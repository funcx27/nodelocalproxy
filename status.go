package main

import (
	"encoding/json"
	"net"
	"net/http"
	"time"
)

// statusServer exposes proxy state on a localhost-only HTTP endpoint.
type statusServer struct {
	listen                string
	pool                  *pool
	backendConnectTimeout time.Duration
	healthCheck           HealthCheck
	started               time.Time
}

func (s *statusServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	return mux
}

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
		Status                string              `json:"status"`
		Listen                string              `json:"listen"`
		Uptime                float64             `json:"uptimeSeconds"`
		BackendConnectTimeout string              `json:"backendConnectTimeout"`
		HealthCheck           healthCheckSnapshot `json:"healthCheck"`
		Backends              []backendSnapshot   `json:"backends"`
	}{
		Status:                healthOverall(anyHealthy),
		Listen:                s.listen,
		Uptime:                time.Since(s.started).Seconds(),
		BackendConnectTimeout: s.backendConnectTimeout.String(),
		HealthCheck:           newHealthCheckSnapshot(s.healthCheck),
		Backends:              backends,
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

type healthCheckSnapshot struct {
	Type               string `json:"type"`
	Path               string `json:"path,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	Interval           string `json:"interval"`
	Timeout            string `json:"timeout"`
	FailureThreshold   int    `json:"failureThreshold"`
	SuccessThreshold   int    `json:"successThreshold"`
}

func newHealthCheckSnapshot(hc HealthCheck) healthCheckSnapshot {
	return healthCheckSnapshot{
		Type:               hc.Type,
		Path:               hc.Path,
		InsecureSkipVerify: hc.InsecureSkipVerify,
		Interval:           hc.Interval.String(),
		Timeout:            hc.Timeout.String(),
		FailureThreshold:   hc.FailureThreshold,
		SuccessThreshold:   hc.SuccessThreshold,
	}
}
