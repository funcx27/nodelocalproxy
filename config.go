package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the YAML configuration loaded from the --config file. The proxy is
// generic: it does not know about kube-apiserver — the listen address, backend
// pool and health checks are all defined here.
//
// A top-level healthCheck may be set once and is inherited by every backend
// that does not specify its own; this is the common case for kube-apiserver,
// where every apiserver is probed identically. Per-backend healthCheck fields
// override the global ones individually.
type Config struct {
	Listen      string       `yaml:"listen"`      // local listen address (typically 127.0.0.1:PORT)
	Status      string       `yaml:"status"`      // status endpoint address (typically 127.0.0.1:PORT)
	HealthCheck *HealthCheck `yaml:"healthCheck"` // global default, inherited by backends
	Backends    []Backend    `yaml:"backends"`    // backend pool (round-robin + failover)
}

// Backend is one upstream the proxy forwards to. In YAML it may be written as a
// bare address string (the common case, inheriting the global healthCheck):
//
//	backends:
//	  - 192.168.100.20:6443
//
// or as an object with its own healthCheck (to override the global default):
//
//	backends:
//	  - address: 192.168.100.21:6443
//	    healthCheck: { type: tcp }
type Backend struct {
	Address     string       `yaml:"address"`
	HealthCheck *HealthCheck `yaml:"healthCheck"`
}

// UnmarshalYAML accepts both the shorthand (a bare address string) and the full
// backend object, so the common apiserver case (identical health checks) keeps
// the config to one line per backend.
func (b *Backend) UnmarshalYAML(value *yaml.Node) error {
	// Shorthand: a scalar node is the address alone.
	if value.Kind == yaml.ScalarNode {
		b.Address = value.Value
		return nil
	}
	// Full object with address + optional healthCheck.
	type rawBackend struct {
		Address     string       `yaml:"address"`
		HealthCheck *HealthCheck `yaml:"healthCheck"`
	}
	var rb rawBackend
	if err := value.Decode(&rb); err != nil {
		return err
	}
	b.Address = rb.Address
	b.HealthCheck = rb.HealthCheck
	return nil
}

// HealthCheck defines how a backend is probed. type "http" (default) issues an
// HTTPS GET to path and treats a 2xx response as healthy; "tcp" only checks the
// TCP port is connectable. insecureSkipVerify defaults to true (a *bool) so the
// minimal config omits it: the kube-apiserver /readyz probe must skip TLS
// verification because apiserver uses a cluster-internal CA the proxy cannot
// trust without distributing that CA. Skipping verification on this read-only
// probe is safe and keeps the proxy certificate-free. Set explicitly to false
// when probing backends whose TLS chain is trusted by the system CA bundle.
type HealthCheck struct {
	Type               string        `yaml:"type"`               // "http" (default) | "tcp"
	Path               string        `yaml:"path"`               // HTTP probe path (e.g. /readyz)
	InsecureSkipVerify *bool         `yaml:"insecureSkipVerify"` // nil → true (default)
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	FailureThreshold   int           `yaml:"failureThreshold"` // consecutive failures → unhealthy
	SuccessThreshold   int           `yaml:"successThreshold"` // consecutive successes → healthy
}

// skipVerify resolves the insecureSkipVerify setting: nil (unset) defaults to
// true so the minimal kube-apiserver config works without the operator opting in
// to skipping verification on every backend. An explicit value is honored.
func (hc *HealthCheck) skipVerify() bool {
	if hc.InsecureSkipVerify == nil {
		return true
	}
	return *hc.InsecureSkipVerify
}

// loadConfig reads and validates the YAML config file.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one backend is required")
	}
	// Effective healthCheck for each backend = global default merged with the
	// backend's own overrides, then field defaults fill any remaining gap. The
	// resolved healthCheck is stored on the backend so the proxy/checker need no
	// knowledge of the merge.
	for i, b := range c.Backends {
		if b.Address == "" {
			return fmt.Errorf("backends[%d].address is required", i)
		}
		effective := mergeHealthCheck(c.HealthCheck, b.HealthCheck)
		effective.applyDefaults()
		c.Backends[i].HealthCheck = effective
	}
	return nil
}

// mergeHealthCheck overlays the backend's healthCheck onto the global default.
// Per-field semantics: a zero value in the override means "inherit from global"
// so a backend can override one field (e.g. type) without restating the rest.
// This mirrors how Kubernetes merges nested config objects.
func mergeHealthCheck(global, override *HealthCheck) *HealthCheck {
	out := &HealthCheck{}
	if global != nil {
		*out = *global
	}
	if override == nil {
		return out
	}
	if override.Type != "" {
		out.Type = override.Type
	}
	if override.Path != "" {
		out.Path = override.Path
	}
	if override.InsecureSkipVerify != nil {
		out.InsecureSkipVerify = override.InsecureSkipVerify
	}
	if override.Interval != 0 {
		out.Interval = override.Interval
	}
	if override.Timeout != 0 {
		out.Timeout = override.Timeout
	}
	if override.FailureThreshold != 0 {
		out.FailureThreshold = override.FailureThreshold
	}
	if override.SuccessThreshold != 0 {
		out.SuccessThreshold = override.SuccessThreshold
	}
	return out
}

func (hc *HealthCheck) applyDefaults() {
	if hc.Type == "" {
		hc.Type = "http"
	}
	if hc.Path == "" {
		hc.Path = "/readyz"
	}
	if hc.Interval == 0 {
		hc.Interval = 3 * time.Second
	}
	if hc.Timeout == 0 {
		hc.Timeout = 1 * time.Second
	}
	if hc.FailureThreshold == 0 {
		hc.FailureThreshold = 2
	}
	if hc.SuccessThreshold == 0 {
		hc.SuccessThreshold = 1
	}
}
