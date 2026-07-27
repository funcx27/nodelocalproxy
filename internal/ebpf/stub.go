//go:build !ebpf

package ebpf

import (
	"context"
	"errors"
	"log/slog"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
)

// Run is the ebpf-transparent entry. Default build has no eBPF; the real
// impl is in runtime.go behind the "ebpf" tag.
func Run(_ context.Context, _ *config.Config, _ *backend.Pool, _ *slog.Logger) error {
	return errors.New("ebpf-transparent mode requires a build with -tags ebpf")
}
