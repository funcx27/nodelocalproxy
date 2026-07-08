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

| type | behavior |
|------|----------|
| `http` (default) | HTTPS GET to `path` (e.g. `/readyz`); 2xx = healthy. Uses `insecureSkipVerify` because apiserver serves a cluster-internal CA. |
| `tcp` | TCP dial the backend port; connect success = healthy. Use when anonymous access to `/readyz` is disabled. |

A backend must pass `successThreshold` consecutive checks to become healthy and
fail `failureThreshold` consecutive checks to become unhealthy — this gives flap
resistance without slow recovery. Even if the health check is stale, per-request
`connect()` failure provides a second layer of failover.

## Config

See [`example-config.yaml`](example-config.yaml). Each backend has its own
`healthCheck` block.

## Build

Pure Go, statically linked (zero glibc dependency):

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nodelocalproxy .
```

Run:

```sh
./nodelocalproxy --config config.yaml
```

Status (localhost-only):

```sh
curl 127.0.0.1:16444/health
```
