//go:build ebpf

package ebpf

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	cgroupPath     = "/sys/fs/cgroup"
	selfCgroupProc = "/proc/self/cgroup"
)

func selfCgroupIDFrom(procSelf, cgroupfsRoot string) (uint64, error) {
	data, err := os.ReadFile(procSelf)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", procSelf, err)
	}
	var rel string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			rel = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if rel == "" {
		return 0, fmt.Errorf("no cgroup v2 entry in %s", procSelf)
	}
	full := filepath.Join(cgroupfsRoot, rel)
	info, err := os.Stat(full)
	if err != nil {
		return 0, fmt.Errorf("stat cgroup %s: %w", full, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("stat: %T", info.Sys())
	}
	return st.Ino, nil
}

func SelfCgroupID() (uint64, error) {
	return selfCgroupIDFrom(selfCgroupProc, cgroupPath)
}
