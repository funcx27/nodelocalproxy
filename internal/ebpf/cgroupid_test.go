//go:build ebpf

package ebpf

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSelfCgroupID(t *testing.T) {
	procSelf := filepath.Join(t.TempDir(), "cgroup")
	os.WriteFile(procSelf, []byte("0::/kubepods.slice/podabc\n"), 0o644)
	root := t.TempDir()
	podDir := filepath.Join(root, "kubepods.slice/podabc")
	os.MkdirAll(podDir, 0o755)

	got, err := selfCgroupIDFrom(procSelf, root)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	info, err := os.Stat(podDir)
	if err != nil {
		t.Fatalf("stat pod dir: %v", err)
	}
	st := info.Sys().(*syscall.Stat_t) //nolint
	if uint64(st.Ino) != got {
		t.Errorf("id = %d, want inode %d", got, st.Ino)
	}
}
