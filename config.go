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
type Config struct {
	Listen   string    `yaml:"listen"`   // local listen address (typically 127.0.0.1:PORT)
	Status   string    `yaml:"status"`   // status endpoint address (typically 127.0.0.1:PORT)
	Backends []Backend `yaml:"backends"` // backend pool (round-robin + failover)
}

// Backend is one upstream the proxy forwards to.
type Backend struct {
	Address     string      `yaml:"address"` // host:port of the upstream
	HealthCheck HealthCheck `yaml:"healthCheck"`
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
	for i, b := range c.Backends {
		if b.Address == "" {
			return fmt.Errorf("backends[%d].address is required", i)
		}
		b.HealthCheck.applyDefaults()
		c.Backends[i].HealthCheck = b.HealthCheck
	}
	return nil
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
