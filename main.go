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
	"strconv"
	"syscall"
	"time"

	"log/slog"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/bootstrap"
	"github.com/funcx27/nodelocalproxy/internal/config"
	"github.com/funcx27/nodelocalproxy/internal/ebpf"
	"github.com/funcx27/nodelocalproxy/internal/proxy"
	"github.com/funcx27/nodelocalproxy/internal/status"
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
		return status.RunCommand(args[1:], stdout, stderr)
	}

	var (
		configPath                string
		logLevel                  string
		bootstrapConfigMapNS      string
		bootstrapConfigMapName    string
		bootstrapConfigMapKey     string
		bootstrapInterceptAddress string
	)
	fs := flag.NewFlagSet("nodelocalproxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&configPath, "config", "", "path to YAML config file; when omitted, bootstrap from an in-cluster ConfigMap")
	fs.StringVar(&logLevel, "log-level", "info", `log level: "debug", "info", "warn", "error"`)
	fs.StringVar(&bootstrapConfigMapNS, "bootstrap-configmap-namespace", "", "namespace for bootstrap ConfigMap; defaults to pod namespace or kube-system")
	fs.StringVar(&bootstrapConfigMapName, "bootstrap-configmap-name", bootstrap.DefaultConfigMapName, "name for bootstrap ConfigMap")
	fs.StringVar(&bootstrapConfigMapKey, "bootstrap-configmap-key", bootstrap.DefaultConfigMapKey, "data key for bootstrap ConfigMap")
	fs.StringVar(&bootstrapInterceptAddress, "bootstrap-intercept-address", "", "intercept host:port used when bootstrap creates a missing ConfigMap")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log := newLogger(logLevel)

	cfg, err := loadRuntimeConfig(context.Background(), configPath, bootstrap.Options{
		ConfigMapNamespace: bootstrapConfigMapNS,
		ConfigMapName:      bootstrapConfigMapName,
		ConfigMapKey:       bootstrapConfigMapKey,
		InterceptAddress:   bootstrapInterceptAddress,
	})
	if err != nil {
		return err
	}
	log.Info("config loaded", "listen", cfg.Listen, "status", cfg.Status, "backends", len(cfg.Backends))

	pool := backend.NewPool(cfg.Backends)
	stats := &proxy.ConnectionStats{}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	checker := backend.NewChecker(pool, cfg.Backends, cfg.HealthCheck, log)
	go checker.Run(ctx)

	statusLn, statusEndpoint, err := proxy.ListenEndpoint(cfg.Status)
	if err != nil {
		log.Error("status listen failed", "addr", cfg.Status, "err", err)
	} else {
		defer func() {
			if err := statusEndpoint.Cleanup(); err != nil && !os.IsNotExist(err) {
				log.Warn("status cleanup failed", "network", statusEndpoint.Network, "addr", statusEndpoint.Address, "err", err)
			}
		}()

		go func() {
			srv := &status.Server{
				Listen:                cfg.Listen,
				Pool:                  pool,
				BackendConnectTimeout: cfg.BackendConnectTimeout,
				HealthCheck:           cfg.HealthCheck,
				Connections:           stats,
				Started:               time.Now(),
				Mode:                  userspaceMode(cfg),
				EbpfStatus:            ebpfStatusClosure(cfg),
			}
			log.Info("status endpoint", "network", statusEndpoint.Network, "addr", statusEndpoint.Address)
			if err := srv.Serve(statusLn); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Error("status server stopped", "err", err)
			}
		}()
	}

	if cfg.IsEbpfMode() {
		log.Info("starting eBPF transparent mode", "intercept", cfg.Intercept.Address)
		return ebpf.Run(ctx, cfg, pool, log)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	log.Info("listening", "addr", cfg.Listen)

	p := proxy.NewProxy(cfg.Listen, cfg.Backends, pool, log, cfg.BackendConnectTimeout, stats)

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.Serve(ctx, ln)
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

func loadRuntimeConfig(ctx context.Context, configPath string, bootstrapOpts bootstrap.Options) (*config.Config, error) {
	if configPath != "" {
		return config.LoadConfig(configPath)
	}
	return bootstrap.LoadOrCreate(ctx, bootstrapOpts)
}

// userspaceMode returns the status Mode value for the configured run mode.
func userspaceMode(cfg *config.Config) string {
	if cfg.IsEbpfMode() {
		return status.ModeEbpfTransparent
	}
	return status.ModeUserspace
}

// ebpfStatusClosure returns the /health EbpfStatus closure. It is nil in
// userspace mode, so the status JSON omits the ebpf block entirely.
func ebpfStatusClosure(cfg *config.Config) func() *status.EbpfStatus {
	if !cfg.IsEbpfMode() {
		return nil
	}
	interceptPort := parseInterceptPort(cfg.Intercept.Address)
	return func() *status.EbpfStatus {
		s := ebpf.Status()
		return &status.EbpfStatus{
			Intercept:     cfg.Intercept.Address,
			InterceptPort: interceptPort,
			Preflight:     s.Preflight,
			BPF: &status.BpfStatus{
				Attached:         s.Attached,
				AttachType:       "cgroup/connect4",
				CgroupPath:       s.CgroupPath,
				SelfExempt:       s.SelfExempt,
				MatchMode:        "backends",
				BackendMapSize:   16,
				LastMapSync:      s.LastSync,
				LastMapSyncError: s.LastSyncErr,
			},
		}
	}
}

// parseInterceptPort extracts the port from a host:port intercept address.
// Returns 0 if the address cannot be parsed.
func parseInterceptPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
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
