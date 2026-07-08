package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestConfigEmbeddedDefaults(t *testing.T) {
	path := writeConfig(t, `
listen: 127.0.0.1:16443
status: 127.0.0.1:16444
backends:
  - 192.168.100.20:6443
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.BackendConnectTimeout != 300*time.Millisecond {
		t.Errorf("backendConnectTimeout: got %v want 300ms", cfg.BackendConnectTimeout)
	}
	hc := cfg.HealthCheck
	if hc.Type != "http" {
		t.Errorf("type: got %q want http", hc.Type)
	}
	if hc.Path != "/readyz" {
		t.Errorf("path: got %q want /readyz", hc.Path)
	}
	if !hc.InsecureSkipVerify {
		t.Error("insecureSkipVerify: want true (embedded default)")
	}
	if hc.Interval != 3*time.Second {
		t.Errorf("interval: got %v want 3s", hc.Interval)
	}
	if hc.Timeout != 1*time.Second {
		t.Errorf("timeout: got %v want 1s", hc.Timeout)
	}
	if hc.FailureThreshold != 2 {
		t.Errorf("failureThreshold: got %d want 2", hc.FailureThreshold)
	}
	if hc.SuccessThreshold != 1 {
		t.Errorf("successThreshold: got %d want 1", hc.SuccessThreshold)
	}
}

func TestConfigHealthCheckMergesWithDefaults(t *testing.T) {
	path := writeConfig(t, `
listen: 127.0.0.1:16443
healthCheck:
  type: http
  path: /livez
backends:
  - 192.168.100.20:6443
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	hc := cfg.HealthCheck
	if hc.Type != "http" {
		t.Errorf("type: got %q want http", hc.Type)
	}
	if hc.Path != "/livez" {
		t.Errorf("path: got %q want /livez", hc.Path)
	}
	if !hc.InsecureSkipVerify {
		t.Error("insecureSkipVerify: want true")
	}
	if hc.Interval != 3*time.Second {
		t.Errorf("interval: got %v want 3s", hc.Interval)
	}
	if hc.Timeout != time.Second {
		t.Errorf("timeout: got %v want 1s", hc.Timeout)
	}
	if hc.FailureThreshold != 2 {
		t.Errorf("failureThreshold: got %d want 2", hc.FailureThreshold)
	}
	if hc.SuccessThreshold != 1 {
		t.Errorf("successThreshold: got %d want 1", hc.SuccessThreshold)
	}
}

func TestConfigBackendConnectTimeoutOverridesDefault(t *testing.T) {
	path := writeConfig(t, `
listen: 127.0.0.1:16443
backendConnectTimeout: 150ms
backends:
  - 192.168.100.20:6443
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.BackendConnectTimeout != 150*time.Millisecond {
		t.Errorf("backendConnectTimeout: got %v want 150ms", cfg.BackendConnectTimeout)
	}
}

func TestConfigBareStringBackends(t *testing.T) {
	path := writeConfig(t, `
listen: 127.0.0.1:16443
backends:
  - 192.168.100.20:6443
  - 192.168.100.21:6443
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Backends) != 2 {
		t.Fatalf("backends: got %d want 2", len(cfg.Backends))
	}
	want := []string{"192.168.100.20:6443", "192.168.100.21:6443"}
	for i, w := range want {
		if cfg.Backends[i] != w {
			t.Errorf("backends[%d]: got %q want %q", i, cfg.Backends[i], w)
		}
	}
}

func TestConfigValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"no listen", `backends: [":6443"]`, "listen is required"},
		{"no backends", `listen: 127.0.0.1:1`, "at least one backend"},
		{"zero backend connect timeout", "listen: 127.0.0.1:1\nbackendConnectTimeout: 0s\nbackends: [\"x:6443\"]", "backendConnectTimeout must be positive"},
		{"bad type", "listen: 127.0.0.1:1\nhealthCheck: {type: bogus, interval: 1s, timeout: 1s, failureThreshold: 1, successThreshold: 1}\nbackends: [\"x:6443\"]", "must be"},
		{"zero interval", "listen: 127.0.0.1:1\nhealthCheck: {type: tcp, interval: 0s, timeout: 1s, failureThreshold: 1, successThreshold: 1}\nbackends: [\"x:6443\"]", "interval must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.yaml)
			_, err := loadConfig(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
		})
	}
}
