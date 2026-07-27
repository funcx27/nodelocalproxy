package config

import (
	_ "embed"
	"fmt"
	"net"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yaml
var defaultConfigYAML []byte

const maxBackends = 16

// Config is the YAML configuration loaded from the --config file.
type Config struct {
	Listen                string        `yaml:"listen"`
	Status                string        `yaml:"status"`
	BackendConnectTimeout time.Duration `yaml:"backendConnectTimeout"`
	HealthCheck           HealthCheck   `yaml:"healthCheck"`
	Backends              []string      `yaml:"backends"`
	Mode                  string        `yaml:"mode"`
	Intercept             Intercept     `yaml:"intercept"`
}

// Intercept configures the eBPF transparent-connect hook.
type Intercept struct {
	Address string `yaml:"address"`
}

// IsEbpfMode reports whether the proxy is running in eBPF transparent-connect mode.
func (c *Config) IsEbpfMode() bool { return c.Mode == "ebpf-transparent" }

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

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return LoadConfigData(data)
}

func LoadConfigData(data []byte) (*Config, error) {
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
	switch c.Mode {
	case "", "userspace", "ebpf-transparent":
	default:
		return fmt.Errorf("mode must be \"userspace\" or \"ebpf-transparent\", got %q", c.Mode)
	}
	if c.IsEbpfMode() {
		if err := c.validateEbpf(); err != nil {
			return err
		}
	} else {
		if err := c.validateUserspace(); err != nil {
			return err
		}
	}
	return c.HealthCheck.validate()
}

func (c *Config) validateUserspace() error {
	if c.Listen == "" {
		return fmt.Errorf("listen is required (userspace mode)")
	}
	return c.validateBackendsCommon(true)
}

func (c *Config) validateEbpf() error {
	if c.Intercept.Address == "" {
		return fmt.Errorf("intercept.address is required (ebpf-transparent mode)")
	}
	if _, _, err := net.SplitHostPort(c.Intercept.Address); err != nil {
		return fmt.Errorf("intercept.address %q: %w", c.Intercept.Address, err)
	}
	return c.validateBackendsCommon(false)
}

func (c *Config) validateBackendsCommon(requireTimeout bool) error {
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one backend is required")
	}
	if len(c.Backends) > maxBackends {
		return fmt.Errorf("backends: got %d, max %d (BPF map limit)", len(c.Backends), maxBackends)
	}
	if requireTimeout && c.BackendConnectTimeout <= 0 {
		return fmt.Errorf("backendConnectTimeout must be positive")
	}
	for i, b := range c.Backends {
		if b == "" {
			return fmt.Errorf("backends[%d] is empty", i)
		}
	}
	return nil
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
