# nodelocalproxy

Per-node local TCP proxy with health-checked backend failover.

## What it does

`nodelocalproxy` runs one instance per node and steers apiserver traffic to a
healthy backend. It supports two modes:

- `userspace`: listen on a local address (typically `127.0.0.1:16443`) and
  forward TCP connections to healthy backends.
- `ebpf-transparent`: attach a `cgroup/connect4` program and rewrite outbound
  connects whose destination `IP:port` matches a configured backend.

Backend selection is round-robin over the **healthy** set. In userspace mode, a
backend `connect()` failure triggers immediate per-request failover to the next
healthy backend.

It is **generic** — the listen address, backend pool and health checks are
driven entirely by a YAML config file. The primary use case is fronting
**kube-apiserver**:

- each node runs one `nodelocalproxy`
- `/etc/hosts` maps the control-plane endpoint hostname to `127.0.0.1`
- the proxy steers traffic across the control-plane nodes' apiservers
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
is not committed. Docker image builds use the checked-in artifacts and do not
install clang, llvm, or bpftool.

```sh
make build           # one binary supporting userspace + ebpf-transparent
make ebpf-generate   # regenerate BPF artifacts
make ebpf-vmlinux    # regenerate vmlinux.h only (when targeting a different kernel)
```

### Config

```yaml
mode: ebpf-transparent

status: unix:///run/nodelocalproxy/status.sock

backends:
  - 192.168.100.20:6443
  - 192.168.100.21:6443
  - 192.168.100.22:6443
```

### How matching works

The BPF hook fires on `connect()`, where the destination has already been
resolved to an IP. It cannot see the domain. The BPF matches only when the
socket destination `IP:port` is one of the configured `backends`; that matched
connection is rewritten to a healthy backend.

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

### All-unhealthy behavior

When every backend is unhealthy, the BPF rejects the connect (`return 0`),
causing `connect()` to fail with `EACCES`. There is no degraded fallthrough knob
in eBPF mode today. If you need fallback to the originally resolved endpoint
when all backends are unhealthy, use `userspace` mode.

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

Image tags:

- Git tag `vX.Y.Z`: `vX.Y.Z`, `latest`
- Pull requests: build only, no push

Release a version:

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

Run:

```sh
./nodelocalproxy --config config.yaml
```

Inside Kubernetes, omitting `--config` enables bootstrap mode. The daemon reads
the `nodelocalproxy` ConfigMap in its own namespace (or `kube-system` if the pod
namespace cannot be detected). If the ConfigMap does not exist, it reads
`default` EndpointSlices labeled `kubernetes.io/service-name=kubernetes`,
creates a config from the ready IPv4 `IP:port` entries, and continues with that
generated config.

Bootstrap creation requires RBAC for `configmaps get/create` in the daemon
namespace and `endpointslices list` in `default`.

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

It prints a short summary and backend table by default. Userspace mode includes
listener and connection counters:

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

eBPF mode hides userspace-only listener and connection counters:

```text
Status: OK
Mode: ebpf-transparent
BPF: attached=true attachType=cgroup/connect4 cgroup=/sys/fs/cgroup selfExempt=true matchMode=backends mapSize=16 lastSync=2026-07-27T14:25:19+08:00
Preflight: cgroupV2=true kernel=6.8.0 btf=true capabilities=true
Uptime: 54s
Backend connect timeout: 300ms
Health check: http /readyz, interval 3s, timeout 1s, thresholds fail=2 success=1

ADDRESS              HEALTH  LAST_CHECK                 LAST_SUCCESS  ERROR
172.16.100.101:6443  OK      2026-07-27T14:25:19+08:00  1s            -
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
