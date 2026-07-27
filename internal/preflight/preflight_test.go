package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCapEff = (uint64(1) << capNetAdmin) | (uint64(1) << capSysAdmin) | (uint64(1) << capBPF)

func TestCgroupV1Rejected(t *testing.T) {
	cg := t.TempDir() // no cgroup.controllers
	_, err := runIn(cg, t.TempDir(), 0, testCapEff, []uint16{6443}, 6443)
	if err == nil {
		t.Fatal("want cgroup v2 error")
	}
}

func TestBtfMissing(t *testing.T) {
	cg := t.TempDir()
	os.WriteFile(filepath.Join(cg, "cgroup.controllers"), []byte("memory\n"), 0o644)
	_, err := runIn(cg, t.TempDir(), 0, testCapEff, []uint16{6443}, 6443) // empty btf dir
	if err == nil {
		t.Fatal("want BTF error")
	}
}

func TestPasses(t *testing.T) {
	cg := t.TempDir()
	os.WriteFile(filepath.Join(cg, "cgroup.controllers"), []byte("memory\n"), 0o644)
	btf := t.TempDir()
	os.WriteFile(filepath.Join(btf, "vmlinux"), []byte("BTF\x00"), 0o644)
	res, err := runIn(cg, btf, 0, testCapEff, []uint16{6443}, 6443)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.CgroupV2 || !res.BTF || !res.Capabilities {
		t.Errorf("result = %+v", res)
	}
}

func TestMissingCapabilitiesRejected(t *testing.T) {
	cg := t.TempDir()
	os.WriteFile(filepath.Join(cg, "cgroup.controllers"), []byte("memory\n"), 0o644)
	btf := t.TempDir()
	os.WriteFile(filepath.Join(btf, "vmlinux"), []byte("BTF\x00"), 0o644)
	_, err := runIn(cg, btf, 0, 0, []uint16{6443}, 6443)
	if err == nil {
		t.Fatal("want capability error")
	}
	if !strings.Contains(err.Error(), "missing effective capabilities") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPortMismatch(t *testing.T) {
	cg := t.TempDir()
	os.WriteFile(filepath.Join(cg, "cgroup.controllers"), []byte("memory\n"), 0o644)
	btf := t.TempDir()
	os.WriteFile(filepath.Join(btf, "vmlinux"), []byte("BTF"), 0o644)
	_, err := runIn(cg, btf, 0, testCapEff, []uint16{6443, 7443}, 6443)
	if err == nil {
		t.Fatal("want port mismatch error")
	}
}

func TestKernelOK(t *testing.T) {
	cases := map[string]bool{"5.4.0": true, "5.15.0": true, "6.1.0": true, "4.19.0": false, "garbage": false}
	for release, want := range cases {
		if got := kernelOK(release); got != want {
			t.Errorf("kernelOK(%q) = %v, want %v", release, got, want)
		}
	}
}
