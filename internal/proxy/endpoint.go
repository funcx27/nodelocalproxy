package proxy

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

// Endpoint describes a parsed listener address (TCP or Unix socket).
type Endpoint struct {
	Network string
	Address string
	Cleanup func() error
}

func ParseEndpoint(raw string) (Endpoint, error) {
	if raw == "" {
		return Endpoint{}, fmt.Errorf("endpoint is required")
	}

	if filepath.IsAbs(raw) {
		return newUnixEndpoint(raw), nil
	}
	if !hasURLScheme(raw) {
		return newTCPEndpoint(raw), nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("parse endpoint: %w", err)
	}

	switch u.Scheme {
	case "unix":
		if u.Path == "" {
			return Endpoint{}, fmt.Errorf("unix socket path is required")
		}
		return newUnixEndpoint(u.Path), nil
	case "tcp":
		if u.Host == "" {
			return Endpoint{}, fmt.Errorf("tcp address is required")
		}
		return newTCPEndpoint(u.Host), nil
	default:
		return Endpoint{}, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
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

func newTCPEndpoint(address string) Endpoint {
	return Endpoint{
		Network: "tcp",
		Address: address,
		Cleanup: func() error {
			return nil
		},
	}
}

func newUnixEndpoint(path string) Endpoint {
	return Endpoint{
		Network: "unix",
		Address: path,
		Cleanup: func() error {
			return os.Remove(path)
		},
	}
}

func ListenEndpoint(raw string) (net.Listener, Endpoint, error) {
	ep, err := ParseEndpoint(raw)
	if err != nil {
		return nil, Endpoint{}, err
	}
	if ep.Network == "unix" {
		if err := os.MkdirAll(filepath.Dir(ep.Address), 0o755); err != nil {
			return nil, Endpoint{}, fmt.Errorf("create socket dir: %w", err)
		}
		if err := ep.Cleanup(); err != nil && !os.IsNotExist(err) {
			return nil, Endpoint{}, fmt.Errorf("remove stale socket %s: %w", ep.Address, err)
		}
	}
	ln, err := net.Listen(ep.Network, ep.Address)
	if err != nil {
		return nil, Endpoint{}, fmt.Errorf("listen %s %s: %w", ep.Network, ep.Address, err)
	}
	return ln, ep, nil
}
