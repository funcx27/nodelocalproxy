package main

import (
	_ "embed"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yaml
var defaultConfigYAML []byte

// Config is the YAML configuration loaded from the --config file. The proxy is
// generic: it does not know about kube-apiserver — the listen address, backend
// pool and health checks are all defined here.
//
// healthCheck applies to every backend uniformly. The common kube-apiserver
// case probes every apiserver identically, so per-backend health checks are not
// supported — to front services needing different probe settings, run multiple
// proxy instances, each with its own config. When healthCheck is omitted the
// embedded defaults.yaml is used; when set, it replaces the default entirely
// (no field-level merge), so every field must be specified.
type Config struct {
	Listen      string      `yaml:"listen"`      // local listen address (typically 127.0.0.1:PORT)
	Status      string      `yaml:"status"`      // status endpoint address (typically 127.0.0.1:PORT)
	HealthCheck HealthCheck `yaml:"healthCheck"` // global probe config applied to all backends
	Backends    []string    `yaml:"backends"`    // backend addresses (host:port), round-robin + failover
}

// HealthCheck defines how each backend is probed. type "http" issues an HTTPS
// GET to path and treats a 2xx response as healthy; "tcp" only checks the TCP
// port is connectable. insecureSkipVerify should be true for the kube-apiserver
// /readyz probe because apiserver uses a cluster-internal CA the proxy cannot
// trust without distributing that CA — skipping verification on this read-only
// probe is safe and keeps the proxy certificate-free.
type HealthCheck struct {
	Type               string        `yaml:"type"`               // "http" | "tcp"
	Path               string        `yaml:"path"`               // HTTP probe path (e.g. /readyz)
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"` // skip TLS verification on http probe
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	FailureThreshold   int           `yaml:"failureThreshold"` // consecutive failures → unhealthy
	SuccessThreshold   int           `yaml:"successThreshold"` // consecutive successes → healthy
}

// loadConfig reads and validates the YAML config file. When the file omits
// healthCheck, the embedded defaults.yaml fills it in.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// No healthCheck in the user file → use the embedded defaults wholesale.
	if c.HealthCheck.Type == "" {
		var d struct {
			HealthCheck HealthCheck `yaml:"healthCheck"`
		}
		if err := yaml.Unmarshal(defaultConfigYAML, &d); err != nil {
			return nil, fmt.Errorf("parse embedded defaults: %w", err)
		}
		c.HealthCheck = d.HealthCheck
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
		if b == "" {
			return fmt.Errorf("backends[%d] is empty", i)
		}
	}
	return c.HealthCheck.validate()
}

func (hc *HealthCheck) validate() error {
	if hc.Type != "http" && hc.Type != "tcp" {
		return fmt.Errorf("healthCheck.type must be \"http\" or \"tcp\", got %q", hc.Type)
	}
	if hc.Interval <= 0 {
		return fmt.Errorf("healthCheck.interval must be positive")
	}
	if hc.Timeout <= 0 {
		return fmt.Errorf("healthCheck.timeout must be positive")
	}
	if hc.FailureThreshold <= 0 {
		return fmt.Errorf("healthCheck.failureThreshold must be positive")
	}
	if hc.SuccessThreshold <= 0 {
		return fmt.Errorf("healthCheck.successThreshold must be positive")
	}
	return nil
}
