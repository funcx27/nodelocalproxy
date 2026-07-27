//go:build ebpf

package ebpf

import "encoding/binary"

// BackendValueLen is the size in bytes of the BPF backend map value.
const BackendValueLen = 8

// EncodeBackend encodes a backend map value in the layout expected by the BPF
// struct backend: 4-byte IPv4 (big-endian) + 2-byte port (big-endian) + 1-byte
// healthy flag + 1-byte padding.
func EncodeBackend(ip4 [4]byte, port uint16, healthy bool) []byte {
	out := make([]byte, BackendValueLen)
	binary.BigEndian.PutUint32(out[0:4], binary.BigEndian.Uint32(ip4[:]))
	binary.BigEndian.PutUint16(out[4:6], port)
	if healthy {
		out[6] = 1
	}
	return out
}
