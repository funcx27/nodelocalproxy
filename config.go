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

// Config is the YAML configuration loaded from the --config file.
type Config struct {
	Listen                string        `yaml:"listen"`
	Status                string        `yaml:"status"`
	BackendConnectTimeout time.Duration `yaml:"backendConnectTimeout"`
	HealthCheck           HealthCheck   `yaml:"healthCheck"`
	Backends              []string      `yaml:"backends"`
}

// HealthCheck defines how each backend is probed.
type HealthCheck struct {
	Type               string        `yaml:"type"`
	Path               string        `yaml:"path"`
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"`
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	FailureThreshold   int           `yaml:"failureThreshold"`
	SuccessThreshold   int           `yaml:"successThreshold"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var c Config
	if err := yaml.Unmarshal(defaultConfigYAML, &c); err != nil {
		return nil, fmt.Errorf("parse embedded defaults: %w", err)
	}
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
	if c.BackendConnectTimeout <= 0 {
		return fmt.Errorf("backendConnectTimeout must be positive")
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
