package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigEmbeddedDefaults verifies that omitting healthCheck uses the embedded
// defaults.yaml (http /readyz, insecureSkipVerify true, 3s/1s, thresholds 2/1).
func TestConfigEmbeddedDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
status: 127.0.0.1:16444
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

// TestConfigExplicitHealthCheckReplaces verifies that setting healthCheck replaces
// the embedded default entirely (no field-level merge): every field comes from
// the user's block.
func TestConfigExplicitHealthCheckReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
healthCheck:
  type: tcp
  interval: 5s
  timeout: 2s
  failureThreshold: 3
  successThreshold: 2
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
	hc := cfg.HealthCheck
	if hc.Type != "tcp" {
		t.Errorf("type: got %q want tcp", hc.Type)
	}
	if hc.Interval != 5*time.Second {
		t.Errorf("interval: got %v want 5s", hc.Interval)
	}
	// path is empty (not in embedded default because user block replaced it),
	// which is fine for tcp probes.
	if hc.Path != "" {
		t.Errorf("path: got %q want empty (no merge)", hc.Path)
	}
}

// TestConfigBareStringBackends verifies backends accept bare address strings.
func TestConfigBareStringBackends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
listen: 127.0.0.1:16443
backends:
  - 192.168.100.20:6443
  - 192.168.100.21:6443
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
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

// TestConfigValidateErrors verifies validation catches the obvious mistakes.
func TestConfigValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"no listen", `backends: [":6443"]`, "listen is required"},
		{"no backends", `listen: 127.0.0.1:1`, "at least one backend"},
		{"bad type", "listen: 127.0.0.1:1\nhealthCheck: {type: bogus, interval: 1s, timeout: 1s, failureThreshold: 1, successThreshold: 1}\nbackends: [\"x:6443\"]", "must be"},
		{"zero interval", "listen: 127.0.0.1:1\nhealthCheck: {type: tcp, interval: 0s, timeout: 1s, failureThreshold: 1, successThreshold: 1}\nbackends: [\"x:6443\"]", "interval must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := loadConfig(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
		})
	}
}
