package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigDefaults verifies the minimal backend config (address + type/path
// only) picks up the documented defaults, in particular insecureSkipVerify=true
// (nil → true) so the kube-apiserver /readyz probe works without opt-in.
func TestConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
status: 127.0.0.1:16444
backends:
  - address: 192.168.100.20:6443
    healthCheck:
      type: http
      path: /readyz
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
	if hc.FailureThreshold != 2 {
		t.Errorf("failureThreshold: got %d want 2", hc.FailureThreshold)
	}
	if hc.SuccessThreshold != 1 {
		t.Errorf("successThreshold: got %d want 1", hc.SuccessThreshold)
	}
	// insecureSkipVerify unset (nil) must default to true.
	if !hc.skipVerify() {
		t.Error("skipVerify(): unset insecureSkipVerify should default to true")
	}
}

// TestConfigExplicitSkipVerify verifies an explicit insecureSkipVerify: false is
// honored (the pointer is non-nil and the value respected).
func TestConfigExplicitSkipVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
backends:
  - address: 192.168.100.20:6443
    healthCheck:
      type: http
      path: /readyz
      insecureSkipVerify: false
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
