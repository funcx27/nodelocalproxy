//go:build ebpf

package ebpf

import (
	"net"
	"testing"
)

func TestParseBackendsIPv4(t *testing.T) {
	got, err := parseBackends([]string{"192.168.100.20:6443", "10.0.0.5:6443"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got[0].ip != ([4]byte{192, 168, 100, 20}) || got[0].port != 6443 {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestParseBackendsRejectsIPv6(t *testing.T) {
	if _, err := parseBackends([]string{"[::1]:6443"}); err == nil {
		t.Fatal("want IPv6 error")
	}
}

func TestEncodeForSync(t *testing.T) {
	var arr [4]byte
	copy(arr[:], net.ParseIP("192.168.100.21").To4())
	if EncodeBackend(arr, 6443, true)[6] != 1 {
		t.Error("healthy byte wrong")
	}
}
