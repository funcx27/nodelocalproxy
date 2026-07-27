package ebpf

import (
	"sync/atomic"
	"time"

	"github.com/funcx27/nodelocalproxy/internal/preflight"
)

// StatusSnapshot is a point-in-time view of the eBPF runtime, published by the
// ebpf build and read by the status package via Status(). The default build
// returns a zero-value snapshot (no runtime is wired).
type StatusSnapshot struct {
	Attached    bool
	CgroupPath  string
	SelfExempt  bool
	LastSync    time.Time
	LastSyncErr string
	Preflight   *preflight.Result
}

// currentStatus holds the latest runtime snapshot. Written only by the ebpf
// runtime build (via setStatus in runtime.go); read by Status() in both
// builds. atomic.Pointer requires no build tag and is safe to reference from
// the default build (it stays nil).
var currentStatus atomic.Pointer[StatusSnapshot]

// Status returns the current eBPF runtime snapshot. In the default build no
// runtime is wired, so currentStatus stays nil and the snapshot is zero.
// The ebpf-tagged runtime.go publishes a live snapshot via setStatus.
func Status() StatusSnapshot {
	if p := currentStatus.Load(); p != nil {
		return *p
	}
	return StatusSnapshot{}
}
