package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	cgroupPath     = "/sys/fs/cgroup"
	btfPath        = "/sys/kernel/btf"
	minKernelMajor = 5
	minKernelMinor = 4
	capNetAdmin    = 12
	capSysAdmin    = 21
	capBPF         = 39
)

// Result captures the outcome of a preflight check run.
type Result struct {
	CgroupV2     bool   `json:"cgroupV2"`
	Kernel       string `json:"kernel"`
	BTF          bool   `json:"btf"`
	Capabilities bool   `json:"capabilities"`
}

// runIn is the testable core of a preflight run. It validates cgroup v2,
// BTF presence, effective capabilities, backend port validity, and kernel
// version against injected paths and process metadata.
func runIn(cgroupRoot, btfRoot string, uid int, capEff uint64, backendPorts []uint16) (*Result, error) {
	if info, err := os.Stat(cgroupRoot); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("preflight: cgroup root %s not a directory", cgroupRoot)
	}
	if _, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return nil, fmt.Errorf("preflight: cgroup v2 required (%s/cgroup.controllers: %w)", cgroupRoot, err)
	}
	btfFile := filepath.Join(btfRoot, "vmlinux")
	if data, err := os.ReadFile(btfFile); err != nil || len(data) == 0 {
		return nil, fmt.Errorf("preflight: BTF required (%s: %w)", btfFile, err)
	}
	for _, p := range backendPorts {
		if p == 0 {
			return nil, fmt.Errorf("preflight: backend port must be positive")
		}
	}
	release := unameRelease()
	if !kernelOK(release) {
		return nil, fmt.Errorf("preflight: kernel %s < 5.4", release)
	}
	if missing := missingCaps(uid, capEff, release); len(missing) > 0 {
		return nil, fmt.Errorf("preflight: missing effective capabilities: %s", strings.Join(missing, ","))
	}
	return &Result{CgroupV2: true, Kernel: release, BTF: true, Capabilities: true}, nil
}

// Run executes the preflight checks against the production cgroup/BTF paths
// and the current effective uid.
func Run(backendPorts []uint16) (*Result, error) {
	capEff, err := readCapEff("/proc/self/status")
	if err != nil {
		return nil, fmt.Errorf("preflight: read effective capabilities: %w", err)
	}
	return runIn(cgroupPath, btfPath, os.Geteuid(), capEff, backendPorts)
}

func readCapEff(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err != nil {
				return 0, err
			}
			return caps, nil
		}
	}
	return 0, fmt.Errorf("CapEff not found")
}

func missingCaps(uid int, capEff uint64, release string) []string {
	required := []struct {
		name string
		bit  int
	}{
		{name: "CAP_NET_ADMIN", bit: capNetAdmin},
		{name: "CAP_SYS_ADMIN", bit: capSysAdmin},
	}
	if kernelAtLeast(release, 5, 8) {
		required = append(required, struct {
			name string
			bit  int
		}{name: "CAP_BPF", bit: capBPF})
	}

	var missing []string
	for _, cap := range required {
		if capEff&(uint64(1)<<cap.bit) == 0 {
			missing = append(missing, cap.name)
		}
	}
	if uid != 0 {
		missing = append(missing, "root")
	}
	return missing
}

// unameRelease returns the running kernel release (e.g. "5.15.0-25-generic")
// or "" if it cannot be determined.
func unameRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// kernelOK reports whether release is at least kernel 5.4.
func kernelOK(release string) bool {
	return kernelAtLeast(release, minKernelMajor, minKernelMinor)
}

func kernelAtLeast(release string, wantMajor, wantMinor int) bool {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(strings.SplitN(parts[1], "-", 2)[0])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > wantMajor || (major == wantMajor && minor >= wantMinor)
}
