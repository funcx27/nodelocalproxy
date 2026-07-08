package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantNetwork string
		wantAddress string
	}{
		{
			name:        "tcp",
			raw:         "127.0.0.1:16444",
			wantNetwork: "tcp",
			wantAddress: "127.0.0.1:16444",
		},
		{
			name:        "tcp scheme",
			raw:         "tcp://127.0.0.1:16444",
			wantNetwork: "tcp",
			wantAddress: "127.0.0.1:16444",
		},
		{
			name:        "unix scheme",
			raw:         "unix:///run/nodelocalproxy/status.sock",
			wantNetwork: "unix",
			wantAddress: "/run/nodelocalproxy/status.sock",
		},
		{
			name:        "absolute unix path",
			raw:         "/run/nodelocalproxy/status.sock",
			wantNetwork: "unix",
			wantAddress: "/run/nodelocalproxy/status.sock",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEndpoint(tc.raw)
			if err != nil {
				t.Fatalf("parseEndpoint: %v", err)
			}
			if got.network != tc.wantNetwork {
				t.Errorf("network: got %q want %q", got.network, tc.wantNetwork)
			}
			if got.address != tc.wantAddress {
				t.Errorf("address: got %q want %q", got.address, tc.wantAddress)
			}
		})
	}
}

func TestParseEndpointEmpty(t *testing.T) {
	if _, err := parseEndpoint(""); err == nil {
		t.Fatal("expected error for empty status endpoint")
	}
}

func TestListenEndpointUnixRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "nodelocalproxy", "status.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create status dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale file stat: %v", err)
	}

	ln, endpoint, err := listenEndpoint("unix://" + path)
	if err != nil {
		t.Fatalf("listenEndpoint: %v", err)
	}
	defer func() {
		if err := endpoint.cleanup(); err != nil && !os.IsNotExist(err) {
			t.Fatalf("cleanup: %v", err)
		}
	}()
	defer func() {
		if err := ln.Close(); err != nil {
			t.Fatalf("close listener: %v", err)
		}
	}()

	if ln.Addr().Network() != "unix" {
		t.Errorf("network: got %q want unix", ln.Addr().Network())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket stat: %v", err)
	}
}

func TestListenEndpointUnixCreatesSocketDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "nodelocalproxy", "status.sock")

	ln, endpoint, err := listenEndpoint("unix://" + path)
	if err != nil {
		t.Fatalf("listenEndpoint: %v", err)
	}
	defer func() {
		if err := endpoint.cleanup(); err != nil && !os.IsNotExist(err) {
			t.Fatalf("cleanup: %v", err)
		}
	}()
	defer func() {
		if err := ln.Close(); err != nil {
			t.Fatalf("close listener: %v", err)
		}
	}()

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("socket dir stat: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket stat: %v", err)
	}
}
