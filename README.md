# nodelocalproxy

Per-node local TCP proxy with health-checked backend failover.

## What it does

`nodelocalproxy` runs one instance per node, listens on a local address
(typically `127.0.0.1:16443`), and forwards each connection to one of a pool of
backends. Backend selection is round-robin over the **healthy** set; a
`connect()` failure triggers immediate per-request failover to the next healthy
backend.

It is **generic** — the listen address, backend pool and health checks are
driven entirely by a YAML config file. The primary use case is fronting
**kube-apiserver**:

- each node runs one `nodelocalproxy`
- `/etc/hosts` maps the control-plane endpoint hostname to `127.0.0.1`
- the proxy load-balances across the control-plane nodes' apiservers
- if one apiserver is down, connections fail over to another within a single
  request, without waiting for the next health-check cycle

## Why 4-layer (not L7 / TLS termination)

kubelet/kubectl ↔ apiserver uses **mutual TLS**: the client presents a client
certificate for identity. A 4-layer (TCP) proxy preserves this end-to-end — the
proxy never sees or terminates TLS, never needs certificates, and never has to
implement the Kubernetes "authenticating proxy" machinery. This is the same
approach used by kubespray's HAProxy templates and RKE2.

## Health checks

A single `healthCheck` block (top-level) applies to every backend uniformly.
The common kube-apiserver case probes every apiserver identically, so per-backend
health checks are intentionally not supported — to front services needing
different probe settings, run multiple proxy instances, each with its own config.

| type | behavior |
|------|----------|
| `http` (default) | HTTPS GET to `path` (e.g. `/readyz`); 2xx = healthy. Uses `insecureSkipVerify` because apiserver serves a cluster-internal CA. |
| `tcp` | TCP dial the backend port; connect success = healthy. Use when anonymous access to `/readyz` is disabled. |

A backend must pass `successThreshold` consecutive checks to become healthy and
fail `failureThreshold` consecutive checks to become unhealthy — this gives flap
resistance without slow recovery. Even if the health check is stale, per-request
`connect()` failure provides a second layer of failover.

Embedded defaults are loaded before the user config, so omitted fields keep their
default values. The default `healthCheck` is http `/readyz`,
`insecureSkipVerify: true`, 3s/1s, thresholds 2/1.

## Config

`mode` defaults to `userspace` — set `listen` + `backends` (bare `host:port`
strings); `backendConnectTimeout` controls per-request backend connect failover.
[`example-config.yaml`](example-config.yaml) shows the `ebpf-transparent` config;
see [eBPF transparent mode](#ebpf-transparent-mode) below.

## Build

Build the default static binary. It supports both `userspace` and
`ebpf-transparent` modes:

```sh
make build
```

Container image:

```sh
make builder
make docker-build IMAGE=nodelocalproxy:dev
```

Multi-arch image:

```sh
make builder
make docker-push IMAGE=your-registry/nodelocalproxy:dev
```

Behind an HTTP proxy:

```sh
make builder PROXY=http://127.0.0.1:10808
make docker-push IMAGE=your-registry/nodelocalproxy:dev PROXY=http://127.0.0.1:10808
```

## eBPF transparent mode

In addition to the userspace proxy, `nodelocalproxy` supports an eBPF
transparent-connect mode (`mode: ebpf-transparent`) that rewrites the
destination address of outbound `connect()` calls at the `cgroup/connect4`
hook, so traffic to the apiserver is steered directly to a healthy backend
without traversing a userspace forwarder.

### Build

`make build` builds the default binary with the `ebpf` tag, so the same
`bin/nodelocalproxy` supports both `userspace` and `ebpf-transparent` modes.
`userspace` mode does not run eBPF preflight or require root/cgroup/BTF. When
`mode: ebpf-transparent` is configured, startup runs preflight and fails fast on
unsupported nodes.

`make ebpf-generate` refreshes the checked-in bpf2go artifacts
(`internal/ebpf/nlp_bpfel.go`, `nlp_bpfel.o`). It needs clang, llvm-strip,
bpftool, and a BTF-capable kernel. `headers/vmlinux.h` is generated locally and
is not committed.

```sh
make build           # one binary supporting userspace + ebpf-transparent
make ebpf-generate   # regenerate BPF artifacts
make ebpf-vmlinux    # regenerate vmlinux.h only (when targeting a different kernel)
```

### Config

```yaml
mode: ebpf-transparent

intercept:
  address: apiserver.example.com:6443

status: unix:///run/nodelocalproxy/status.sock

backends:
  - 192.168.100.20:6443
  - 192.168.100.21:6443
  - 192.168.100.22:6443
```

### How matching works

The BPF hook fires on `connect()`, where the destination has already been
resolved to an IP — it cannot see the domain. So:

- `intercept.address` is the apiserver **domain** the client actually dials. It
  is used for port extraction and display only.
- The BPF matches when `dst IP:port ∈ backends` (and `dst port ==
  intercept.address`'s port). Each worker node may resolve the apiserver domain
  to a different control-plane IP via local `/etc/hosts` or DNS; as long as the
  resolved IP:port appears in `backends`, the connect is intercepted and
  rewritten to a healthy backend.

### TLS / cert SAN

The BPF rewrites the TCP layer; the TLS `serverName` is taken from the client
URL host and is unaffected by rewriting. Therefore the cert SAN must contain
the **apiserver domain** (e.g. `apiserver.example.com`), not a node IP. Do not
point kubeconfig `server` at a single control-plane node IP — a rewrite to a
different control-plane node would then fail cert validation. Use the apiserver
domain as the kubeconfig server.

### Fallback semantics

The BPF link is not pinned; it lives only as long as the daemon process holds
its FD. If the daemon is not running, has not attached, or has exited, new
connects fall through to whatever the local resolver returned for the apiserver
domain — i.e. the node's configured control-plane IP, which must itself be
reachable. While the daemon is attached and all backends are unhealthy, the
connect is **denied** (no fallthrough) — see below.

### `rejectOnAllUnhealthy` is deny-only in MVP

When every backend is unhealthy, the BPF rejects the connect (`return 0`),
causing `connect()` to fail with `EACCES`. The configurable
`rejectOnAllUnhealthy=false` fallthrough path is **not** implemented in the
current MVP; the behavior is effectively always deny. This is intentional for
the first release and documented as a known limitation. If you need degraded
fallthrough, run in `userspace` mode until the runtime-tunable is added.

### Deploy

See [`deploy/daemonset-ebpf.yaml`](deploy/daemonset-ebpf.yaml): runs as a
per-node DaemonSet with capabilities `BPF,NET_ADMIN,SYS_ADMIN` (fall back to
`privileged: true` if the container runtime rejects fine-grained caps),
hostPath mounts for `/sys/fs/cgroup` and `/sys/kernel/btf` (both read-only),
`hostNetwork: true`, a status-based liveness probe, and `maxUnavailable: 1`
for rolling updates. The daemon does not pin BPF objects, so a Pod restart
detaches the old link automatically.

GitHub Actions publishes multi-arch images to GHCR:

```sh
ghcr.io/funcx27/nodelocalproxy
```

Version tags:

- Git tag `v0.1.0`: `v0.1.0`, `latest`
- Pull requests: build only, no push

Release a version:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Run:

```sh
./nodelocalproxy --config config.yaml
```

Inside Kubernetes, omitting `--config` enables bootstrap mode. The daemon reads
the `nodelocalproxy` ConfigMap in its own namespace (or `kube-system` if the pod
namespace cannot be detected). If the ConfigMap does not exist, it reads
`default/kubernetes` Endpoints, creates a config from the ready IPv4 addresses,
and continues with that generated config:

```sh
./nodelocalproxy --bootstrap-intercept-address apiserver.example.com:6443
```

Bootstrap creation requires RBAC for `configmaps get/create` in the daemon
namespace and `endpoints get` in `default`. Once the ConfigMap exists,
`--bootstrap-intercept-address` is not required.

Status defaults to a Unix socket. The parent directory is created
automatically:

```yaml
status: unix:///run/nodelocalproxy/status.sock
```

```sh
curl --unix-socket /run/nodelocalproxy/status.sock http://localhost/health
```

The same status can be queried with the built-in command, without requiring
`curl`, `jq` or `column`:

```sh
./nodelocalproxy status
```

It prints a short summary and backend table by default:

```text
Status: OK
Listen: 127.0.0.1:16443
Uptime: 5m12s
Backend connect timeout: 300ms
Connections: 2/128/2 (ACTIVE/TOTAL/FAILED)
Health check: http /readyz, interval 3s, timeout 1s, thresholds fail=2 success=1

ADDRESS        HEALTH  CONNECTIONS  LAST_CHECK                 LAST_SUCCESS  ERROR
10.0.0.1:6443  OK      1/72/0      2026-07-14T15:04:05+08:00  0s                -
10.0.0.2:6443  OK      1/56/0      2026-07-14T15:04:05+08:00  0s                -
```

By default the command uses the built-in Unix status endpoint:

```sh
./nodelocalproxy status
```

Pass the daemon config when the status endpoint is customized. This supports
both Unix sockets and TCP status endpoints:

```sh
./nodelocalproxy status --config config.yaml
```

Use `--json` to print the raw health JSON.

Status can be exposed on TCP when needed:

```yaml
status: tcp://127.0.0.1:16444
```

```sh
curl 127.0.0.1:16444/health
```

`host:port` is also accepted for compatibility.
