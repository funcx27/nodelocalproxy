package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"log/slog"
)

type checker struct {
	pool     *pool
	backends []string
	hc       HealthCheck
	log      *slog.Logger
}

func newChecker(p *pool, backends []string, hc HealthCheck, log *slog.Logger) *checker {
	return &checker{pool: p, backends: backends, hc: hc, log: log}
}

func (c *checker) run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, addr := range c.backends {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			c.loop(ctx, addr)
		}(addr)
	}
	wg.Wait()
}

func (c *checker) loop(ctx context.Context, addr string) {
	t := time.NewTicker(c.hc.Interval)
	defer t.Stop()

	c.probe(ctx, addr)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.probe(ctx, addr)
		}
	}
}

func (c *checker) probe(ctx context.Context, addr string) {
	hc := c.hc
	pctx, cancel := context.WithTimeout(ctx, hc.Timeout)
	defer cancel()

	idx := c.pool.index(addr)
	if idx < 0 {
		return
	}
	err := c.doProbe(pctx, addr)
	now := time.Now()

	s := c.pool.states[idx]
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCheck = now

	if err == nil {
		s.success++
		s.fails = 0
		s.lastErr = ""
		if s.success >= hc.SuccessThreshold {
			previous := s.health
			s.health = healthHealthy
			if previous != healthHealthy {
				c.log.Info("backend recovered", "backend", addr, "index", idx)
			}
		}
		return
	}

	s.fails++
	s.success = 0
	s.lastErr = errToString(err)
	if s.fails >= hc.FailureThreshold {
		previous := s.health
		s.health = healthUnhealthy
		if previous != healthUnhealthy {
			c.log.Warn("backend unhealthy", "backend", addr, "index", idx, "err", err, "consecutive", s.fails)
		}
	}
}

func (c *checker) doProbe(ctx context.Context, addr string) error {
	switch c.hc.Type {
	case "tcp":
		return c.doTCPProbe(ctx, addr)
	default:
		return c.doHTTPProbe(ctx, addr)
	}
}

func (c *checker) doTCPProbe(ctx context.Context, addr string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	if err := conn.Close(); err != nil {
		c.log.Debug("tcp probe close failed", "backend", addr, "err", err)
	}
	return nil
}

func (c *checker) doHTTPProbe(ctx context.Context, addr string) error {
	client := &http.Client{
		Timeout: 0, // governed by ctx
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: c.hc.InsecureSkipVerify, //nolint:gosec // cluster-internal read-only probe
			},
		},
	}
	url := fmt.Sprintf("https://%s%s", addr, c.hc.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.log.Debug("probe response body close failed", "backend", addr, "err", err)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("probe %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}
