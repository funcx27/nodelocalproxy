package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
	"github.com/funcx27/nodelocalproxy/internal/preflight"
	"github.com/funcx27/nodelocalproxy/internal/proxy"
)

func TestStatusHealthIncludesHealthCheck(t *testing.T) {
	p := backend.NewPool([]string{"127.0.0.1:6443"})
	markAllHealthy(p)
	stats := &proxy.ConnectionStats{}
	stats.Open()
	stats.Connect()

	hc := config.HealthCheck{
		Type:               "http",
		Path:               "/readyz",
		InsecureSkipVerify: true,
		Interval:           3 * time.Second,
		Timeout:            time.Second,
		FailureThreshold:   2,
		SuccessThreshold:   1,
	}
	srv := &Server{
		Listen:                "127.0.0.1:16443",
		Pool:                  p,
		BackendConnectTimeout: 300 * time.Millisecond,
		HealthCheck:           hc,
		Connections:           stats,
		Started:               time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.HandleHealth(rec, req)

	var got struct {
		BackendConnectTimeout string                   `json:"backendConnectTimeout"`
		HealthCheck           HealthCheckSnapshot      `json:"healthCheck"`
		Connections           proxy.ConnectionSnapshot `json:"connections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.BackendConnectTimeout != "300ms" {
		t.Errorf("backendConnectTimeout: got %q want 300ms", got.BackendConnectTimeout)
	}
	if got.HealthCheck.Type != "http" {
		t.Errorf("type: got %q want http", got.HealthCheck.Type)
	}
	if got.HealthCheck.Path != "/readyz" {
		t.Errorf("path: got %q want /readyz", got.HealthCheck.Path)
	}
	if got.HealthCheck.Interval != "3s" {
		t.Errorf("interval: got %q want 3s", got.HealthCheck.Interval)
	}
	if got.HealthCheck.Timeout != "1s" {
		t.Errorf("timeout: got %q want 1s", got.HealthCheck.Timeout)
	}
	if got.HealthCheck.FailureThreshold != 2 {
		t.Errorf("failureThreshold: got %d want 2", got.HealthCheck.FailureThreshold)
	}
	if got.HealthCheck.SuccessThreshold != 1 {
		t.Errorf("successThreshold: got %d want 1", got.HealthCheck.SuccessThreshold)
	}
	if !got.HealthCheck.InsecureSkipVerify {
		t.Error("insecureSkipVerify: want true")
	}
	if got.Connections.Active != 1 {
		t.Errorf("connections.active: got %d want 1", got.Connections.Active)
	}
	if got.Connections.Total != 1 {
		t.Errorf("connections.total: got %d want 1", got.Connections.Total)
	}
	if got.Connections.Connected != 1 {
		t.Errorf("connections.connected: got %d want 1", got.Connections.Connected)
	}
	if strings.Contains(rec.Body.String(), `"fails"`) || strings.Contains(rec.Body.String(), `"success":`) {
		t.Errorf("backend threshold counters must not be exposed: %s", rec.Body.String())
	}
}

func TestStatusHealthUserspaceMode(t *testing.T) {
	p := backend.NewPool([]string{"127.0.0.1:6443"})
	stats := &proxy.ConnectionStats{}
	srv := &Server{
		Listen:      "127.0.0.1:16443",
		Pool:        p,
		Connections: stats,
		Started:     time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.HandleHealth(rec, req)

	var got struct {
		Mode string          `json:"mode"`
		BPF  json.RawMessage `json:"bpf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Mode != ModeUserspace {
		t.Errorf("mode: got %q want %q", got.Mode, ModeUserspace)
	}
	if len(got.BPF) > 0 && string(got.BPF) != "null" {
		t.Errorf("bpf: userspace must omit, got %s", got.BPF)
	}
}

func TestStatusHealthEbpfClosureInjection(t *testing.T) {
	p := backend.NewPool([]string{"127.0.0.1:6443"})
	stats := &proxy.ConnectionStats{}
	pf := &preflight.Result{CgroupV2: true, Kernel: "5.15.0", BTF: true}
	lastSync := time.Now().Add(-5 * time.Second)

	srv := &Server{
		Listen:      "127.0.0.1:16443",
		Pool:        p,
		Connections: stats,
		Started:     time.Now(),
		Mode:        ModeEbpfTransparent,
		EbpfStatus: func() *EbpfStatus {
			return &EbpfStatus{
				Preflight: pf,
				BPF: &BpfStatus{
					Attached:         true,
					AttachType:       "cgroup/connect4",
					CgroupPath:       "/sys/fs/cgroup",
					SelfExempt:       true,
					MatchMode:        "backends",
					BackendMapSize:   16,
					LastMapSync:      lastSync,
					LastMapSyncError: "",
				},
			}
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.HandleHealth(rec, req)

	var got struct {
		Mode      string            `json:"mode"`
		Preflight *preflight.Result `json:"preflight"`
		BPF       *struct {
			Attached         bool      `json:"attached"`
			AttachType       string    `json:"attachType"`
			CgroupPath       string    `json:"cgroupPath"`
			SelfExempt       bool      `json:"selfExempt"`
			MatchMode        string    `json:"matchMode"`
			BackendMapSize   int       `json:"backendMapSize"`
			LastMapSync      time.Time `json:"lastMapSync"`
			LastMapSyncError string    `json:"lastMapSyncError"`
		} `json:"bpf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Mode != ModeEbpfTransparent {
		t.Errorf("mode: got %q want %q", got.Mode, ModeEbpfTransparent)
	}
	if got.Preflight == nil || !got.Preflight.CgroupV2 {
		t.Errorf("preflight: got %+v", got.Preflight)
	}
	if got.BPF == nil {
		t.Fatal("bpf: expected non-nil")
	}
	if !got.BPF.Attached {
		t.Error("bpf.attached: want true")
	}
	if got.BPF.AttachType != "cgroup/connect4" {
		t.Errorf("bpf.attachType: got %q", got.BPF.AttachType)
	}
	if got.BPF.MatchMode != "backends" {
		t.Errorf("bpf.matchMode: got %q", got.BPF.MatchMode)
	}
	if got.BPF.BackendMapSize != 16 {
		t.Errorf("bpf.backendMapSize: got %d want 16", got.BPF.BackendMapSize)
	}
}

func TestStatusHealthEbpfNilClosure(t *testing.T) {
	// If the closure returns nil, ebpf fields must be omitted even in ebpf mode.
	p := backend.NewPool([]string{"127.0.0.1:6443"})
	stats := &proxy.ConnectionStats{}
	srv := &Server{
		Listen:      "127.0.0.1:16443",
		Pool:        p,
		Connections: stats,
		Started:     time.Now(),
		Mode:        ModeEbpfTransparent,
		EbpfStatus:  func() *EbpfStatus { return nil },
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.HandleHealth(rec, req)

	var got struct {
		Mode string          `json:"mode"`
		BPF  json.RawMessage `json:"bpf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Mode != ModeEbpfTransparent {
		t.Errorf("mode: got %q want %q", got.Mode, ModeEbpfTransparent)
	}
	if len(got.BPF) > 0 && string(got.BPF) != "null" {
		t.Errorf("bpf: should be omitted, got %s", got.BPF)
	}
}

func TestPrintHealthTableEbpf(t *testing.T) {
	var buf strings.Builder
	health := HealthResponse{
		Status:    "ok",
		Mode:      ModeEbpfTransparent,
		Preflight: &preflight.Result{CgroupV2: true, Kernel: "5.15.0", BTF: true},
		BPF: &BpfStatus{
			Attached:       true,
			AttachType:     "cgroup/connect4",
			CgroupPath:     "/sys/fs/cgroup",
			SelfExempt:     true,
			MatchMode:      "backends",
			BackendMapSize: 16,
		},
		Backends: []backend.BackendSnapshot{
			{
				Address:     "10.0.0.1:6443",
				Health:      "healthy",
				Connections: backend.BackendConnectionSnapshot{Active: 1, Total: 7, Failed: 0},
			},
		},
	}
	if err := printHealthTable(&buf, health); err != nil {
		t.Fatalf("printHealthTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Mode: ebpf-transparent", "BPF:", "attached=true", "Preflight:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nGot:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Listen:") {
		t.Errorf("ebpf mode must suppress Listen line\nGot:\n%s", out)
	}
	if strings.Contains(out, "Intercept:") {
		t.Errorf("ebpf mode must suppress Intercept line\nGot:\n%s", out)
	}
	for _, hidden := range []string{"CONNECTIONS", "FAILS"} {
		if strings.Contains(out, hidden) {
			t.Errorf("ebpf mode must suppress %s column\nGot:\n%s", hidden, out)
		}
	}
}
