//go:build ignore

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>

#define MAX_BACKENDS 16

struct backend {
    __u32 ip4;       /* network order */
    __u16 port;      /* network order */
    __u8  healthy;
    __u8  _pad;
};

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, MAX_BACKENDS);
    __type(key, __u32);
    __type(value, struct backend);
} backends SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u32);
} rr_cursor SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u64);
} self_cgid SEC(".maps");

const volatile __u32 backend_count  = 0;

SEC("cgroup/connect4")
int cgroup_connect4(struct bpf_sock_addr *ctx)
{
    __u32 zero = 0;

    __u64 *self = bpf_map_lookup_elem(&self_cgid, &zero);
    if (self && *self == bpf_get_current_cgroup_id())
        return 1; /* exempt daemon's own cgroup */

    __u32 bound = backend_count;
    if (bound > MAX_BACKENDS)
        bound = MAX_BACKENDS;

    bool matched = false;
    for (__u32 i = 0; i < bound; i++) {
        __u32 key = i;
        struct backend *b = bpf_map_lookup_elem(&backends, &key);
        if (b && ctx->user_ip4 == b->ip4 && ctx->user_port == b->port) {
            matched = true;
            break;
        }
    }
    if (!matched)
        return 1;

    __u32 *cur = bpf_map_lookup_elem(&rr_cursor, &zero);
    __u32 start = cur ? *cur : 0;
    for (__u32 i = 0; i < bound; i++) {
        __u32 idx = (start + i) % bound;
        __u32 key = idx;
        struct backend *b = bpf_map_lookup_elem(&backends, &key);
        if (b && b->healthy) {
            ctx->user_ip4  = b->ip4;
            ctx->user_port = b->port;
            __u32 next = idx + 1;
            bpf_map_update_elem(&rr_cursor, &zero, &next, BPF_ANY);
            return 1;
        }
    }
    return 0; /* all unhealthy: deny */
}

char _license[] SEC("license") = "GPL";
