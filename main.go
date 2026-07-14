// Command nodelocalproxy is a per-node TCP proxy with health-checked backend
// failover.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "nodelocalproxy: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "status" {
		return runStatusCommand(args[1:], stdout, stderr)
	}

	var (
		configPath string
		logLevel   string
	)
	fs := flag.NewFlagSet("nodelocalproxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&configPath, "config", "", "path to YAML config file (required)")
	fs.StringVar(&logLevel, "log-level", "info", `log level: "debug", "info", "warn", "error"`)
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	stats := &connectionStats{}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	checker := newChecker(pool, cfg.Backends, cfg.HealthCheck, log)
	go checker.run(ctx)

	statusLn, statusEndpoint, err := listenEndpoint(cfg.Status)
	if err != nil {
		log.Error("status listen failed", "addr", cfg.Status, "err", err)
	} else {
		defer func() {
			if err := statusEndpoint.cleanup(); err != nil && !os.IsNotExist(err) {
				log.Warn("status cleanup failed", "network", statusEndpoint.network, "addr", statusEndpoint.address, "err", err)
			}
		}()

		go func() {
			srv := &statusServer{
				listen:                cfg.Listen,
				pool:                  pool,
				backendConnectTimeout: cfg.BackendConnectTimeout,
				healthCheck:           cfg.HealthCheck,
				connections:           stats,
				started:               time.Now(),
			}
			log.Info("status endpoint", "network", statusEndpoint.network, "addr", statusEndpoint.address)
			if err := srv.serve(statusLn); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Error("status server stopped", "err", err)
			}
		}()
	}

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
		dialTimeout: cfg.BackendConnectTimeout,
		stats:       stats,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.serve(ctx, ln)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		if statusLn != nil {
			_ = statusLn.Close()
		}
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
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:     lv,
		AddSource: true,
	}))
}
