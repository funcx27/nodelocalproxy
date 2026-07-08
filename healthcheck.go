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

// checker continuously probes each backend and updates pool health. One goroutine
// per backend runs the probe on the shared healthCheck interval; the proxy is
// unavailable only until the first successful (or threshold-failing) probe lands.
type checker struct {
	pool     *pool
	backends []string
	hc       HealthCheck
	log      *slog.Logger
}

func newChecker(p *pool, backends []string, hc HealthCheck, log *slog.Logger) *checker {
	return &checker{pool: p, backends: backends, hc: hc, log: log}
}

// run starts one probe goroutine per backend and blocks until ctx is cancelled.
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

	// Probe once immediately so health is known before the first interval elapses.
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

// probe runs a single check and applies the success/failure thresholds to the
// pool. A healthy probe must reach successThreshold consecutive times; an
// unhealthy probe demotes after failureThreshold consecutive times. This gives
// flap resistance without slow recovery.
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

// doProbe performs the actual check. "http" issues an HTTPS GET (the kube-apiserver
// serves /readyz over HTTPS with its cluster CA, hence insecureSkipVerify);
// "tcp" only dials the port. Any non-2xx HTTP status is treated as failure.
func (c *checker) doProbe(ctx context.Context, addr string) error {
	switch c.hc.Type {
	case "tcp":
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	default: // "http"
		client := &http.Client{
			Timeout: 0, // governed by ctx
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: c.hc.InsecureSkipVerify, //nolint:gosec // intentional: cluster-internal CA on read-only probe
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
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("probe %s: HTTP %d", url, resp.StatusCode)
		}
		return nil
	}
}
