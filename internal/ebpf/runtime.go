//go:build ebpf

package ebpf

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"

	"github.com/funcx27/nodelocalproxy/internal/backend"
	"github.com/funcx27/nodelocalproxy/internal/config"
	"github.com/funcx27/nodelocalproxy/internal/preflight"
)

// testDisableSelfExempt, when set by integration builds, forces the daemon's
// self cgroup id written into the BPF map to a sentinel (0) that never matches
// bpf_get_current_cgroup_id() (kernel cgroup inodes are always >= 1). This
// disables self-exemption so the test process's own connects become observable
// through the rewriting path. It is always compiled into ebpf builds and
// defaults to off → zero production impact. Toggled only via
// TestSetDisableSelfExempt in integration builds.
var testDisableSelfExempt atomic.Bool

type parsedBackend struct {
	ip   [4]byte
	port uint16
}

func parseBackends(addrs []string) ([]parsedBackend, error) {
	out := make([]parsedBackend, 0, len(addrs))
	for _, a := range addrs {
		host, portStr, err := net.SplitHostPort(a)
		if err != nil {
			return nil, fmt.Errorf("backend %q: %w", a, err)
		}
		ip4 := net.ParseIP(host).To4()
		if ip4 == nil {
			return nil, fmt.Errorf("backend %q: not IPv4", a)
		}
		var arr [4]byte
		copy(arr[:], ip4)
		var port16 uint16
		fmt.Sscanf(portStr, "%d", &port16)
		if port16 == 0 {
			return nil, fmt.Errorf("backend %q: bad port", a)
		}
		out = append(out, parsedBackend{ip: arr, port: port16})
	}
	return out, nil
}

type Runtime struct {
	objs   *NlpObjects
	lk     link.Link
	log    *slog.Logger
	pool   *backend.Pool
	parsed []parsedBackend

	LastSync    time.Time
	LastSyncErr string

	cgroupPath string
	selfExempt bool
	preflight  *preflight.Result
}

// setStatus replaces the current snapshot. ebpf-build only.
func setStatus(s StatusSnapshot) {
	cp := s
	currentStatus.Store(&cp)
}

// publish replaces the published snapshot with the runtime's current state.
func (r *Runtime) publish() {
	if r == nil {
		return
	}
	setStatus(StatusSnapshot{
		Attached:    r.lk != nil,
		CgroupPath:  r.cgroupPath,
		SelfExempt:  r.selfExempt,
		LastSync:    r.LastSync,
		LastSyncErr: r.LastSyncErr,
		Preflight:   r.preflight,
	})
}

func Run(ctx context.Context, cfg *config.Config, pool *backend.Pool, log *slog.Logger) error {
	_, portStr, err := net.SplitHostPort(cfg.Intercept.Address)
	if err != nil {
		return fmt.Errorf("intercept.address: %w", err)
	}
	var interceptPort uint16
	fmt.Sscanf(portStr, "%d", &interceptPort)

	parsed, err := parseBackends(cfg.Backends)
	if err != nil {
		return err
	}
	ports := make([]uint16, len(parsed))
	for i, p := range parsed {
		ports[i] = p.port
	}
	pfResult, err := preflight.Run(interceptPort, ports)
	if err != nil {
		return err
	}

	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("preflight: remove memlock limit: %w", err)
	}

	spec, err := LoadNlp()
	if err != nil {
		return fmt.Errorf("load bpf spec: %w", err)
	}
	if v := spec.Variables["intercept_port"]; v == nil {
		return fmt.Errorf("bpf variable intercept_port missing")
	} else if err := v.Set(htons(interceptPort)); err != nil {
		return fmt.Errorf("set intercept_port: %w", err)
	}
	if v := spec.Variables["backend_count"]; v == nil {
		return fmt.Errorf("bpf variable backend_count missing")
	} else if err := v.Set(uint32(len(parsed))); err != nil {
		return fmt.Errorf("set backend_count: %w", err)
	}
	var objs NlpObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return fmt.Errorf("load bpf objects: %w", err)
	}

	rt := &Runtime{objs: &objs, log: log, pool: pool, parsed: parsed, cgroupPath: cgroupPath, selfExempt: true, preflight: pfResult}
	for i, p := range parsed {
		if err := objs.Backends.Put(uint32(i), EncodeBackend(p.ip, p.port, false)); err != nil {
			objs.Close()
			return fmt.Errorf("populate backends[%d]: %w", i, err)
		}
	}
	cgid, err := SelfCgroupID()
	if err != nil {
		objs.Close()
		return fmt.Errorf("self cgroup id: %w", err)
	}
	// Test hook: when integration tests disable self-exemption, overwrite the
	// cgid with sentinel 0 (kernel cgroup inodes are always >= 1, so 0 never
	// matches bpf_get_current_cgroup_id()), so the BPF equality check fails and
	// the daemon's own connects are subject to rewriting. Note: 1 is NOT a safe
	// sentinel — the root cgroup (/sys/fs/cgroup) commonly has inode 1.
	if testDisableSelfExempt.Load() {
		cgid = 0
		rt.selfExempt = false
	}
	if err := objs.SelfCgid.Put(uint32(0), cgid); err != nil {
		objs.Close()
		return fmt.Errorf("write self_cgid: %w", err)
	}

	lk, err := link.AttachCgroup(link.CgroupOptions{
		Path:    cgroupPath,
		Attach:  ebpf.AttachCGroupInet4Connect,
		Program: objs.NlpPrograms.CgroupConnect4,
	})
	if err != nil {
		objs.Close()
		return fmt.Errorf("attach cgroup/connect4: %w", err)
	}
	rt.lk = lk
	rt.publish()
	log.Info("bpf attached", "cgroup", cgroupPath)

	go rt.syncLoop(ctx)
	<-ctx.Done()
	log.Info("shutting down eBPF runtime")
	return rt.Close()
}

func (r *Runtime) syncLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	r.syncOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.syncOnce()
		}
	}
}

func (r *Runtime) syncOnce() {
	snap := r.pool.Snapshot()
	for i, b := range snap {
		if i >= len(r.parsed) {
			break
		}
		healthy := b.Health == backend.HealthHealthy.String()
		if err := r.objs.Backends.Put(uint32(i), EncodeBackend(r.parsed[i].ip, r.parsed[i].port, healthy)); err != nil {
			r.LastSyncErr = err.Error()
			r.log.Error("map sync failed", "idx", i, "err", err)
			r.publish()
			return
		}
	}
	r.LastSync = time.Now()
	r.LastSyncErr = ""
	r.publish()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	if r.lk != nil {
		_ = r.lk.Close()
		r.lk = nil
	}
	if r.objs != nil {
		_ = r.objs.Close()
		r.objs = nil
	}
	// Publish a detached snapshot so status reflects the closed runtime;
	// otherwise the last attached snapshot lingers and observers (liveness
	// probe, status command, integration tests) see a stale "attached" state.
	r.publish()
	return nil
}

// htons yields the value that, when stored in the host-native byte order used
// by VariableSpec.Set and then read by the BPF program on a little-endian
// target, matches bpf_sock_addr.user_port. user_port is a __u32 in network
// (big-endian) byte order; for a given port the BPF side reads it as the low
// __u16 0xA046 (for port 18080 = 0x46A0), so intercept_port must hold the
// identical bit pattern 0xA046.
//
// RESOLVED by the integration test (TestIntegrationConnectRewritten): this
// swap form is correct — confirmed by the all-unhealthy → connect denied and
// the A-unhealthy → failover-to-B disambiguation. Do NOT change to identity.
func htons(v uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return binary.LittleEndian.Uint16(b[:])
}
