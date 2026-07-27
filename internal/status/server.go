package status

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
	"github.com/funcx27/nodelocalproxy/internal/preflight"
	"github.com/funcx27/nodelocalproxy/internal/proxy"
)

// Mode constants surfaced in the /health JSON.
const (
	ModeUserspace       = "userspace"
	ModeEbpfTransparent = "ebpf-transparent"
)

// HealthResponse is the JSON payload returned by the /health endpoint and
// consumed by the status CLI. Defined once here so producer and consumer
// share one type — future field additions are compile-checked on both sides.
type HealthResponse struct {
	Status                string                    `json:"status"`
	Mode                  string                    `json:"mode"`
	Listen                string                    `json:"listen"`
	Uptime                float64                   `json:"uptimeSeconds"`
	BackendConnectTimeout string                    `json:"backendConnectTimeout"`
	HealthCheck           HealthCheckSnapshot       `json:"healthCheck"`
	Connections           proxy.ConnectionSnapshot  `json:"connections"`
	Backends              []backend.BackendSnapshot `json:"backends"`
	Intercept             string                    `json:"intercept,omitempty"`
	InterceptPort         int                       `json:"interceptPort,omitempty"`
	Preflight             *preflight.Result         `json:"preflight,omitempty"`
	BPF                   *BpfStatus                `json:"bpf,omitempty"`
}

// EbpfStatus is the JSON view of the eBPF-mode runtime state. Populated only
// when the server is wired with an EbpfStatus closure (i.e. ebpf mode).
type EbpfStatus struct {
	Intercept     string            `json:"intercept"`
	InterceptPort int               `json:"interceptPort"`
	Preflight     *preflight.Result `json:"preflight"`
	BPF           *BpfStatus        `json:"bpf"`
}

// BpfStatus is the JSON view of the eBPF runtime snapshot.
type BpfStatus struct {
	Attached         bool      `json:"attached"`
	AttachType       string    `json:"attachType"`
	CgroupPath       string    `json:"cgroupPath"`
	SelfExempt       bool      `json:"selfExempt"`
	MatchMode        string    `json:"matchMode"`
	BackendMapSize   int       `json:"backendMapSize"`
	LastMapSync      time.Time `json:"lastMapSync"`
	LastMapSyncError string    `json:"lastMapSyncError"`
}

// Server exposes proxy state on a localhost-only HTTP endpoint.
type Server struct {
	Listen                string
	Pool                  *backend.Pool
	BackendConnectTimeout time.Duration
	HealthCheck           config.HealthCheck
	Connections           *proxy.ConnectionStats
	Started               time.Time

	// Mode is the run mode surfaced in the /health JSON (userspace or
	// ebpf-transparent). Defaults to ModeUserspace when empty.
	Mode string

	// EbpfStatus, when non-nil, is called on each /health request to gather
	// the eBPF-mode runtime snapshot. Nil in userspace mode.
	EbpfStatus func() *EbpfStatus
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.HandleHealth)
	return mux
}

func (s *Server) HandleHealth(w http.ResponseWriter, _ *http.Request) {
	backends := s.Pool.Snapshot()
	anyHealthy := false
	for _, b := range backends {
		if b.Health == backend.HealthHealthy.String() {
			anyHealthy = true
			break
		}
	}
	resp := HealthResponse{
		Status:                healthOverall(anyHealthy),
		Mode:                  s.mode(),
		Listen:                s.Listen,
		Uptime:                time.Since(s.Started).Seconds(),
		BackendConnectTimeout: s.BackendConnectTimeout.String(),
		HealthCheck:           NewHealthCheckSnapshot(s.HealthCheck),
		Connections:           s.Connections.Snapshot(),
		Backends:              backends,
	}
	if s.EbpfStatus != nil {
		if es := s.EbpfStatus(); es != nil {
			resp.Intercept = es.Intercept
			resp.InterceptPort = es.InterceptPort
			resp.Preflight = es.Preflight
			resp.BPF = es.BPF
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) mode() string {
	if s.Mode == "" {
		return ModeUserspace
	}
	return s.Mode
}

func healthOverall(anyHealthy bool) string {
	if anyHealthy {
		return "ok"
	}
	return "degraded"
}

// Serve serves HTTP health requests on the supplied listener.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{Handler: s.routes()}
	return srv.Serve(ln)
}

// HealthCheckSnapshot is the JSON view of config.HealthCheck.
type HealthCheckSnapshot struct {
	Type               string `json:"type"`
	Path               string `json:"path,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	Interval           string `json:"interval"`
	Timeout            string `json:"timeout"`
	FailureThreshold   int    `json:"failureThreshold"`
	SuccessThreshold   int    `json:"successThreshold"`
}

// NewHealthCheckSnapshot builds a HealthCheckSnapshot from cfg.
func NewHealthCheckSnapshot(hc config.HealthCheck) HealthCheckSnapshot {
	return HealthCheckSnapshot{
		Type:               hc.Type,
		Path:               hc.Path,
		InsecureSkipVerify: hc.InsecureSkipVerify,
		Interval:           hc.Interval.String(),
		Timeout:            hc.Timeout.String(),
		FailureThreshold:   hc.FailureThreshold,
		SuccessThreshold:   hc.SuccessThreshold,
	}
}
