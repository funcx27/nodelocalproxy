// Command nodelocalproxy is a per-node, local TCP proxy with health-checked
// backend failover. It is primarily used to front kube-apiserver: each node
// runs one instance, /etc/hosts points the control-plane endpoint at 127.0.0.1,
// and the proxy load-balances across the control-plane nodes' apiservers.
//
// The proxy itself is generic — the listen address, backend pool and health
// checks are driven entirely by a YAML config file, so it can front any service
// that needs a per-node local proxy with failover.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nodelocalproxy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath string
		logLevel   string
	)
	flag.StringVar(&configPath, "config", "", "path to YAML config file (required)")
	flag.StringVar(&logLevel, "log-level", "info", `log level: "debug", "info", "warn", "error"`)
	flag.Parse()

	if configPath == "" {
		return fmt.Errorf("--config is required")
	}

	log := newLogger(logLevel)

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	log.Info("config loaded", "listen", cfg.Listen, "status", cfg.Status, "backends", len(cfg.Backends))

	pool := newPool(cfg.Backends)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Health checker runs for the lifetime of the process; cancelling ctx stops it.
	checker := newChecker(pool, cfg.Backends, cfg.HealthCheck, log)
	go checker.run(ctx)

	// Status endpoint: localhost-only HTTP for operator inspection.
	go func() {
		ln, err := net.Listen("tcp", cfg.Status)
		if err != nil {
			log.Error("status listen failed", "addr", cfg.Status, "err", err)
			return
		}
		srv := &statusServer{listen: cfg.Listen, pool: pool, started: time.Now()}
		log.Info("status endpoint", "addr", cfg.Status)
		if err := srv.serve(ln); err != nil {
			log.Error("status server stopped", "err", err)
		}
	}()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	log.Info("listening", "addr", cfg.Listen)

	p := &proxy{
		listen:      cfg.Listen,
		backends:    cfg.Backends,
		pool:        pool,
		log:         log,
		dialTimeout: defaultDialTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.serve(ctx, ln)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		_ = ln.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}
