package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigGlobalDefaultInherited verifies a backend written as a bare address
// string inherits the top-level healthCheck, and field defaults fill the gaps.
func TestConfigGlobalDefaultInherited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
status: 127.0.0.1:16444
healthCheck:
  type: http
  path: /readyz
backends:
  - 192.168.100.20:6443
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Backends[0].Address != "192.168.100.20:6443" {
		t.Errorf("address: got %q want 192.168.100.20:6443", cfg.Backends[0].Address)
	}
	hc := cfg.Backends[0].HealthCheck
	if hc.Type != "http" {
		t.Errorf("type: got %q want http", hc.Type)
	}
	if hc.Path != "/readyz" {
		t.Errorf("path: got %q want /readyz", hc.Path)
	}
	if hc.Interval != 3*time.Second {
		t.Errorf("interval: got %v want 3s", hc.Interval)
	}
	if hc.Timeout != 1*time.Second {
		t.Errorf("timeout: got %v want 1s", hc.Timeout)
	}
	if !hc.skipVerify() {
		t.Error("skipVerify(): unset insecureSkipVerify should default to true")
	}
}

// TestConfigBackendOverride verifies a per-backend healthCheck field overrides the
// global default for that field only, while unset fields still inherit.
func TestConfigBackendOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
healthCheck:
  type: http
  path: /readyz
  interval: 5s
backends:
  - address: 192.168.100.20:6443
    healthCheck: { type: tcp }
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	hc := cfg.Backends[0].HealthCheck
	// type overridden to tcp.
	if hc.Type != "tcp" {
		t.Errorf("type: got %q want tcp", hc.Type)
	}
	// path inherited from global.
	if hc.Path != "/readyz" {
		t.Errorf("path: got %q want /readyz (inherited)", hc.Path)
	}
	// interval inherited from global.
	if hc.Interval != 5*time.Second {
		t.Errorf("interval: got %v want 5s (inherited)", hc.Interval)
	}
}

// TestConfigExplicitSkipVerify verifies an explicit insecureSkipVerify: false is
// honored, even when set at the global level.
func TestConfigExplicitSkipVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
healthCheck:
  type: http
  path: /readyz
  insecureSkipVerify: false
backends:
  - 192.168.100.20:6443
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Backends[0].HealthCheck.skipVerify() {
		t.Error("skipVerify(): explicit false should be honored")
	}
}

// TestConfigNoGlobalDefault verifies backends work with no global healthCheck —
// every field falls back to the built-in defaults (http /readyz etc.).
func TestConfigNoGlobalDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
backends:
  - 192.168.100.20:6443
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	hc := cfg.Backends[0].HealthCheck
	if hc.Type != "http" {
		t.Errorf("type: got %q want http (built-in default)", hc.Type)
	}
	if hc.Path != "/readyz" {
		t.Errorf("path: got %q want /readyz (built-in default)", hc.Path)
	}
}

// TestConfigMixedBackends verifies a mix of bare-string and full-object backends
// all resolve correctly against a global default.
func TestConfigMixedBackends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
healthCheck: { type: http, path: /readyz }
backends:
  - 192.168.100.20:6443
  - address: 192.168.100.21:6443
    healthCheck: { type: tcp }
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Backends[0].Address != "192.168.100.20:6443" {
		t.Errorf("backend 0 address: %q", cfg.Backends[0].Address)
	}
	if cfg.Backends[1].Address != "192.168.100.21:6443" {
		t.Errorf("backend 1 address: %q", cfg.Backends[1].Address)
	}
	if cfg.Backends[0].HealthCheck.Type != "http" {
		t.Errorf("backend 0 type: got %q want http (inherited)", cfg.Backends[0].HealthCheck.Type)
	}
	if cfg.Backends[1].HealthCheck.Type != "tcp" {
		t.Errorf("backend 1 type: got %q want tcp (overridden)", cfg.Backends[1].HealthCheck.Type)
	}
}
