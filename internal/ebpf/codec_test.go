//go:build ebpf

package ebpf

import (
	"encoding/binary"
	"testing"
)

func TestEncodeBackendLayout(t *testing.T) {
	got := EncodeBackend([4]byte{192, 168, 100, 21}, 6443, true)
	if len(got) != BackendValueLen {
		t.Fatalf("len = %d, want %d", len(got), BackendValueLen)
	}
	wantIP := uint32(192)<<24 | 168<<16 | 100<<8 | 21
	if binary.BigEndian.Uint32(got[0:4]) != wantIP {
		t.Errorf("ip4 = %x, want %x", got[0:4], wantIP)
	}
	if binary.BigEndian.Uint16(got[4:6]) != 6443 {
		t.Errorf("port = %x, want 6443", got[4:6])
	}
	if got[6] != 1 || got[7] != 0 {
		t.Errorf("healthy/pad = %d/%d, want 1/0", got[6], got[7])
	}
}

func TestEncodeBackendUnhealthy(t *testing.T) {
	got := EncodeBackend([4]byte{10, 0, 0, 1}, 443, false)
	if len(got) != BackendValueLen {
		t.Fatalf("len = %d, want %d", len(got), BackendValueLen)
	}
	if got[6] != 0 {
		t.Errorf("healthy = %d, want 0", got[6])
	}
	if got[7] != 0 {
		t.Errorf("pad = %d, want 0", got[7])
	}
}
