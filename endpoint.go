package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

type endpoint struct {
	network string
	address string
	cleanup func() error
}

func parseEndpoint(raw string) (endpoint, error) {
	if raw == "" {
		return endpoint{}, fmt.Errorf("endpoint is required")
	}

	if filepath.IsAbs(raw) {
		return newUnixEndpoint(raw), nil
	}
	if !hasURLScheme(raw) {
		return newTCPEndpoint(raw), nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return endpoint{}, fmt.Errorf("parse endpoint: %w", err)
	}

	switch u.Scheme {
	case "unix":
		if u.Path == "" {
			return endpoint{}, fmt.Errorf("unix socket path is required")
		}
		return newUnixEndpoint(u.Path), nil
	case "tcp":
		if u.Host == "" {
			return endpoint{}, fmt.Errorf("tcp address is required")
		}
		return newTCPEndpoint(u.Host), nil
	default:
		return endpoint{}, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
}

func hasURLScheme(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] == ':' {
			return i+2 < len(raw) && raw[i+1] == '/' && raw[i+2] == '/'
		}
	}
	return false
}

func newTCPEndpoint(address string) endpoint {
	return endpoint{
		network: "tcp",
		address: address,
		cleanup: func() error {
			return nil
		},
	}
}

func newUnixEndpoint(path string) endpoint {
	return endpoint{
		network: "unix",
		address: path,
		cleanup: func() error {
			return os.Remove(path)
		},
	}
}

func listenEndpoint(raw string) (net.Listener, endpoint, error) {
	ep, err := parseEndpoint(raw)
	if err != nil {
		return nil, endpoint{}, err
	}
	if ep.network == "unix" {
		if err := os.MkdirAll(filepath.Dir(ep.address), 0o755); err != nil {
			return nil, endpoint{}, fmt.Errorf("create socket dir: %w", err)
		}
		if err := ep.cleanup(); err != nil && !os.IsNotExist(err) {
			return nil, endpoint{}, fmt.Errorf("remove stale socket %s: %w", ep.address, err)
		}
	}
	ln, err := net.Listen(ep.network, ep.address)
	if err != nil {
		return nil, endpoint{}, fmt.Errorf("listen %s %s: %w", ep.network, ep.address, err)
	}
	return ln, ep, nil
}
