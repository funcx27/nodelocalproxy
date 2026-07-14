package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusHealthIncludesHealthCheck(t *testing.T) {
	p := newPool([]string{"127.0.0.1:6443"})
	markAllHealthy(p)
	stats := &connectionStats{}
	stats.open()
	stats.connect()

	hc := HealthCheck{
		Type:               "http",
		Path:               "/readyz",
		InsecureSkipVerify: true,
		Interval:           3 * time.Second,
		Timeout:            time.Second,
		FailureThreshold:   2,
		SuccessThreshold:   1,
	}
	srv := &statusServer{
		listen:                "127.0.0.1:16443",
		pool:                  p,
		backendConnectTimeout: 300 * time.Millisecond,
		healthCheck:           hc,
		connections:           stats,
		started:               time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.handleHealth(rec, req)

	var got struct {
		BackendConnectTimeout string              `json:"backendConnectTimeout"`
		HealthCheck           healthCheckSnapshot `json:"healthCheck"`
		Connections           connectionSnapshot  `json:"connections"`
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
}
