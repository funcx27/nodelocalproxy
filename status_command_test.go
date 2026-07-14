package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunStatusCommandPrintsTable(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "status.sock")
	closeServer := serveStatusFixture(t, socket, healthResponse{
		Status:                "degraded",
		Listen:                "127.0.0.1:16443",
		Uptime:                125,
		BackendConnectTimeout: "300ms",
		Connections: connectionSnapshot{
			Active:    1,
			Total:     12,
			Connected: 10,
			Failed:    2,
		},
		HealthCheck: healthCheckSnapshot{
			Type:             "http",
			Path:             "/readyz",
			Interval:         "3s",
			Timeout:          "1s",
			FailureThreshold: 2,
			SuccessThreshold: 1,
		},
		Backends: []backendSnapshot{
			{
				Index:       0,
				Address:     "10.0.0.1:6443",
				Health:      "healthy",
				Fails:       0,
				Success:     3,
				Connections: backendConnectionSnapshot{Active: 1, Total: 7, Failed: 0},
			},
			{
				Index:       1,
				Address:     "10.0.0.2:6443",
				Health:      "unhealthy",
				Fails:       2,
				Success:     0,
				LastErr:     "connection refused",
				Connections: backendConnectionSnapshot{Active: 0, Total: 3, Failed: 2},
			},
		},
	})
	defer closeServer()
	configPath := writeStatusConfig(t, "unix://"+socket)

	var out bytes.Buffer
	var stderr bytes.Buffer
	err := runStatusCommand([]string{"--config", configPath}, &out, &stderr)
	if err != nil {
		t.Fatalf("run status command: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"ADDRESS",
		"HEALTH",
		"Status: DEGRADED",
		"Listen: 127.0.0.1:16443",
		"Uptime: 2m5s",
		"Connections: 1/12/2 (ACTIVE/TOTAL/FAILED)",
		"Health check: http /readyz",
		"10.0.0.1:6443",
		"OK",
		"1/7/0",
		"10.0.0.2:6443",
		"BAD",
		"0/3/2",
		"connection refused",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "INDEX") {
		t.Fatalf("output should not include INDEX column:\n%s", got)
	}
}

func TestRunStatusCommandPrintsRawJSON(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "status.sock")
	closeServer := serveStatusFixture(t, socket, healthResponse{
		Status: "ok",
		Backends: []backendSnapshot{
			{Index: 0, Address: "10.0.0.1:6443", Health: "healthy"},
		},
	})
	defer closeServer()
	configPath := writeStatusConfig(t, "unix://"+socket)

	var out bytes.Buffer
	var stderr bytes.Buffer
	err := runStatusCommand([]string{"--config", configPath, "--json"}, &out, &stderr)
	if err != nil {
		t.Fatalf("run status command: %v", err)
	}

	var got healthResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode raw JSON: %v\n%s", err, out.String())
	}
	if got.Status != "ok" {
		t.Fatalf("status: got %q want ok", got.Status)
	}
}

func TestPrintHealthTableShowsZeroConnections(t *testing.T) {
	var out bytes.Buffer
	err := printHealthTable(&out, healthResponse{
		Status: "ok",
		Backends: []backendSnapshot{
			{Index: 0, Address: "10.0.0.1:6443", Health: "healthy"},
		},
	})
	if err != nil {
		t.Fatalf("print health table: %v", err)
	}
	if !strings.Contains(out.String(), "Connections: 0/0/0 (ACTIVE/TOTAL/FAILED)") {
		t.Fatalf("output missing zero connection stats:\n%s", out.String())
	}
}

func TestRunStatusCommandSupportsTCPStatusEndpoint(t *testing.T) {
	srv := httptest.NewServer(statusFixtureHandler(t, healthResponse{
		Status: "ok",
		Backends: []backendSnapshot{
			{Index: 0, Address: "10.0.0.1:6443", Health: "healthy"},
		},
	}))
	defer srv.Close()

	configPath := writeStatusConfig(t, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))

	var out bytes.Buffer
	var stderr bytes.Buffer
	err := runStatusCommand([]string{"--config", configPath}, &out, &stderr)
	if err != nil {
		t.Fatalf("run status command: %v", err)
	}
	if !strings.Contains(out.String(), "Status: OK") {
		t.Fatalf("output missing TCP status response:\n%s", out.String())
	}
}

func TestValidateUnixSocketRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := validateUnixSocket(path); err == nil {
		t.Fatal("expected regular file to be rejected")
	}
}

func serveStatusFixture(t *testing.T, socket string, health healthResponse) func() {
	t.Helper()

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}

	srv := &http.Server{
		Handler: statusFixtureHandler(t, health),
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown fixture server: %v", err)
		}
		if err := <-errCh; err != nil && err != http.ErrServerClosed {
			t.Fatalf("serve fixture: %v", err)
		}
	}
}

func statusFixtureHandler(t *testing.T, health healthResponse) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(health); err != nil {
			t.Errorf("encode fixture response: %v", err)
		}
	})
}

func writeStatusConfig(t *testing.T, status string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("listen: 127.0.0.1:16443\nstatus: " + status + "\nbackends:\n  - 10.0.0.1:6443\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
