package status

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
	"github.com/funcx27/nodelocalproxy/internal/proxy"
)

const (
	defaultStatusEndpoint = "unix:///run/nodelocalproxy/status.sock"
	statusHealthPath      = "/health"
	statusRequestTimeout  = 2 * time.Second
)

// HealthResponse is defined in server.go and shared by producer and consumer.

// RunCommand executes the `nodelocalproxy status` subcommand.
func RunCommand(args []string, stdout, stderr io.Writer) error {
	var (
		configPath string
		rawJSON    bool
	)
	fs := flag.NewFlagSet("nodelocalproxy status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&configPath, "config", "", "path to YAML config file; defaults to the built-in Unix status socket")
	fs.BoolVar(&rawJSON, "json", false, "print raw health JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	statusEndpoint := defaultStatusEndpoint
	if configPath != "" {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			return err
		}
		statusEndpoint = cfg.Status
	}

	ctx, cancel := context.WithTimeout(context.Background(), statusRequestTimeout)
	defer cancel()

	body, err := fetchHealth(ctx, statusEndpoint)
	if err != nil {
		return err
	}
	if rawJSON {
		_, err := stdout.Write(body)
		return err
	}

	var health HealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		return fmt.Errorf("decode health JSON: %w", err)
	}
	return printHealthTable(stdout, health)
}

func validateUnixSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("nodelocalproxy status socket not found: %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("nodelocalproxy status socket is not a socket: %s", path)
	}
	return nil
}

func fetchHealth(ctx context.Context, rawEndpoint string) ([]byte, error) {
	ep, err := proxy.ParseEndpoint(rawEndpoint)
	if err != nil {
		return nil, err
	}

	url := "http://localhost" + statusHealthPath
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ep.Network == "unix" {
		if err := validateUnixSocket(ep.Address); err != nil {
			return nil, err
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", ep.Address)
		}
	} else {
		url = "http://" + ep.Address + statusHealthPath
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create status request: %w", err)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request status health: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("request status health: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read status health: %w", err)
	}
	return body, nil
}

func printHealthTable(w io.Writer, health HealthResponse) error {
	if _, err := fmt.Fprintf(w, "Status: %s\n", strings.ToUpper(defaultString(health.Status, "unknown"))); err != nil {
		return err
	}
	mode := health.Mode
	if mode == "" {
		mode = ModeUserspace
	}
	if _, err := fmt.Fprintf(w, "Mode: %s\n", mode); err != nil {
		return err
	}
	isEbpf := mode == ModeEbpfTransparent
	if isEbpf {
		if health.Intercept != "" {
			if _, err := fmt.Fprintf(w, "Intercept: %s\n", formatIntercept(health.Intercept, health.InterceptPort)); err != nil {
				return err
			}
		}
		if health.BPF != nil {
			if _, err := fmt.Fprintf(w, "BPF: attached=%t attachType=%s cgroup=%s selfExempt=%t matchMode=%s mapSize=%d lastSync=%s%s\n",
				health.BPF.Attached,
				defaultString(health.BPF.AttachType, "-"),
				defaultString(health.BPF.CgroupPath, "-"),
				health.BPF.SelfExempt,
				defaultString(health.BPF.MatchMode, "-"),
				health.BPF.BackendMapSize,
				formatLastCheck(health.BPF.LastMapSync),
				formatSyncErr(health.BPF.LastMapSyncError),
			); err != nil {
				return err
			}
		}
		if health.Preflight != nil {
			if _, err := fmt.Fprintf(w, "Preflight: cgroupV2=%t kernel=%s btf=%t capabilities=%t\n",
				health.Preflight.CgroupV2,
				defaultString(health.Preflight.Kernel, "-"),
				health.Preflight.BTF,
				health.Preflight.Capabilities,
			); err != nil {
				return err
			}
		}
	}
	// Userspace-only lines: hide Listen/Connections in ebpf-transparent mode
	// (the proxy does not own a listen socket; connections flow via the BPF hook).
	if !isEbpf {
		if health.Listen != "" {
			if _, err := fmt.Fprintf(w, "Listen: %s\n", health.Listen); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "Connections: %s (ACTIVE/TOTAL/FAILED)\n",
			formatConnections(health.Connections.Active, health.Connections.Total, health.Connections.Failed),
		); err != nil {
			return err
		}
	}
	if health.Uptime > 0 {
		if _, err := fmt.Fprintf(w, "Uptime: %s\n", formatSeconds(health.Uptime)); err != nil {
			return err
		}
	}
	if health.BackendConnectTimeout != "" {
		if _, err := fmt.Fprintf(w, "Backend connect timeout: %s\n", health.BackendConnectTimeout); err != nil {
			return err
		}
	}
	if health.HealthCheck.Type != "" {
		if _, err := fmt.Fprintf(w, "Health check: %s%s, interval %s, timeout %s, thresholds fail=%d success=%d\n",
			health.HealthCheck.Type,
			formatHealthPath(health.HealthCheck.Path),
			health.HealthCheck.Interval,
			health.HealthCheck.Timeout,
			health.HealthCheck.FailureThreshold,
			health.HealthCheck.SuccessThreshold,
		); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if isEbpf {
		if _, err := fmt.Fprintln(tw, "ADDRESS\tHEALTH\tLAST_CHECK\tLAST_SUCCESS\tERROR"); err != nil {
			return err
		}
		for _, b := range health.Backends {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				b.Address,
				formatBackendHealth(b.Health),
				formatLastCheck(b.LastCheck),
				formatAgo(b.LastSuccess),
				defaultString(b.LastErr, "-"),
			); err != nil {
				return err
			}
		}
		return tw.Flush()
	}

	if _, err := fmt.Fprintln(tw, "ADDRESS\tHEALTH\tCONNECTIONS\tLAST_CHECK\tLAST_SUCCESS\tERROR"); err != nil {
		return err
	}
	for _, b := range health.Backends {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			b.Address,
			formatBackendHealth(b.Health),
			formatBackendConnections(b.Connections),
			formatLastCheck(b.LastCheck),
			formatAgo(b.LastSuccess),
			defaultString(b.LastErr, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func formatIntercept(addr string, port int) string {
	if port > 0 {
		return fmt.Sprintf("%s (port %d)", addr, port)
	}
	return addr
}

func formatSyncErr(err string) string {
	if err == "" {
		return ""
	}
	return " err=" + err
}

func formatHealthPath(path string) string {
	if path == "" {
		return ""
	}
	return " " + path
}

func formatSeconds(seconds float64) string {
	return (time.Duration(seconds) * time.Second).Round(time.Second).String()
}

func formatLastCheck(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}

func formatAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	elapsed := time.Since(t).Round(time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.String()
}

func formatBackendHealth(health string) string {
	switch strings.ToLower(health) {
	case "healthy":
		return "OK"
	case "unhealthy":
		return "BAD"
	case "unknown", "":
		return "UNKNOWN"
	default:
		return strings.ToUpper(health)
	}
}

func formatBackendConnections(connections backend.BackendConnectionSnapshot) string {
	return formatConnections(connections.Active, connections.Total, connections.Failed)
}

func formatConnections(active int64, total, failed uint64) string {
	return fmt.Sprintf("%d/%d/%d", active, total, failed)
}
