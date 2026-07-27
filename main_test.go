package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/funcx27/nodelocalproxy/internal/config"
)

// writeConfigHelper writes the given YAML content to a temp file and returns
// its path. Mirrors the helper in internal/config/config_test.go.
func writeConfigHelper(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestEbpfConfigDoesNotRequireListen guards the routing branch in run(): an
// ebpf-mode config has no listen address, yet must load and report ebpf mode.
func TestEbpfConfigDoesNotRequireListen(t *testing.T) {
	path := writeConfigHelper(t, "mode: ebpf-transparent\nintercept:\n  address: a.example:6443\nbackends: [\"1.2.3.4:6443\"]\n")
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.IsEbpfMode() {
		t.Fatal("expected ebpf mode")
	}
}
