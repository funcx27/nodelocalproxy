//go:build ebpf && integration

package ebpf

// TestSetDisableSelfExempt toggles the test-only hook that disables the
// daemon's self cgroup exemption. With exemption disabled, connects from the
// daemon's own cgroup are subject to BPF rewriting, allowing integration tests
// to observe rewriting from the test process that hosts ebpf.Run. With
// exemption enabled (default), daemon connects bypass rewriting, matching
// production semantics for the health checker.
//
// Production builds never reference this symbol; it exists only under the
// integration build tag.
func TestSetDisableSelfExempt(v bool) { testDisableSelfExempt.Store(v) }
