package main

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
)

const (
	defaultStatusEndpoint = "unix:///run/nodelocalproxy/status.sock"
	statusHealthPath      = "/health"
	statusRequestTimeout  = 2 * time.Second
)

type healthResponse struct {
	Status                string              `json:"status"`
	Listen                string              `json:"listen"`
	Uptime                float64             `json:"uptimeSeconds"`
	BackendConnectTimeout string              `json:"backendConnectTimeout"`
	HealthCheck           healthCheckSnapshot `json:"healthCheck"`
	Connections           connectionSnapshot  `json:"connections"`
	Backends              []backendSnapshot   `json:"backends"`
}

func runStatusCommand(args []string, stdout, stderr io.Writer) error {
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
		cfg, err := loadConfig(configPath)
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

	var health healthResponse
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
	ep, err := parseEndpoint(rawEndpoint)
	if err != nil {
		return nil, err
	}

	url := "http://localhost" + statusHealthPath
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ep.network == "unix" {
		if err := validateUnixSocket(ep.address); err != nil {
			return nil, err
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", ep.address)
		}
	} else {
		url = "http://" + ep.address + statusHealthPath
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

func printHealthTable(w io.Writer, health healthResponse) error {
	if _, err := fmt.Fprintf(w, "Status: %s\n", strings.ToUpper(defaultString(health.Status, "unknown"))); err != nil {
		return err
	}
	if health.Listen != "" {
		if _, err := fmt.Fprintf(w, "Listen: %s\n", health.Listen); err != nil {
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
	if _, err := fmt.Fprintf(w, "Connections: %s (ACTIVE/TOTAL/FAILED)\n",
		formatConnections(health.Connections.Active, health.Connections.Total, health.Connections.Failed),
	); err != nil {
		return err
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
	if _, err := fmt.Fprintln(tw, "ADDRESS\tHEALTH\tCONNECTIONS\tFAILS\tSUCCESS\tCHECKED\tERROR"); err != nil {
		return err
	}
	for _, backend := range health.Backends {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			backend.Address,
			formatBackendHealth(backend.Health),
			formatBackendConnections(backend.Connections),
			backend.Fails,
			backend.Success,
			formatLastCheck(backend.LastCheck),
			defaultString(backend.LastErr, "-"),
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

func formatBackendConnections(connections backendConnectionSnapshot) string {
	return formatConnections(connections.Active, connections.Total, connections.Failed)
}

func formatConnections(active int64, total, failed uint64) string {
	return fmt.Sprintf("%d/%d/%d", active, total, failed)
}
