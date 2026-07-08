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

See [`example-config.yaml`](example-config.yaml). Backends are bare address
strings; `backendConnectTimeout` controls per-request backend connect failover.

## Build

Pure Go, statically linked (zero glibc dependency):

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nodelocalproxy .
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

Status (localhost-only):

```sh
curl 127.0.0.1:16444/health
```
