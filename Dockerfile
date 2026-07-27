# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25.5
ARG BPFTOOL_VERSION=7.7.0

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build

# clang/llvm/kernel headers + bpftool compile/generate the eBPF program
# (vmlinux.h from BTF) under the ebpf build tag during `go build`.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates clang curl llvm linux-libc-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

ARG BPFTOOL_VERSION
ARG TARGETOS TARGETARCH HTTP_PROXY HTTPS_PROXY http_proxy https_proxy

RUN set -eux; \
    case "$TARGETARCH" in \
      amd64|arm64) bpftool_arch="$TARGETARCH" ;; \
      *) echo "unsupported TARGETARCH for bpftool: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    proxy="${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}"; \
    curl_proxy_opts=""; \
    if [ -n "$proxy" ]; then curl_proxy_opts="-x $proxy"; fi; \
    base_url="https://github.com/libbpf/bpftool/releases/download/v${BPFTOOL_VERSION}"; \
    archive="bpftool-v${BPFTOOL_VERSION}-${bpftool_arch}.tar.gz"; \
    curl $curl_proxy_opts -fsSLo "/tmp/${archive}" "${base_url}/${archive}"; \
    curl $curl_proxy_opts -fsSLo "/tmp/${archive}.sha256sum" "${base_url}/${archive}.sha256sum"; \
    cd /tmp; \
    sha256sum -c "${archive}.sha256sum"; \
    tar -xzf "${archive}"; \
    install -m 0755 bpftool /usr/local/bin/bpftool; \
    rm -f "/tmp/${archive}" "/tmp/${archive}.sha256sum" /tmp/bpftool

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Generate vmlinux.h from the builder kernel's BTF. Required: vmlinux.h is
# gitignored, so the build must regenerate it. A kernel without BTF fails
# loudly here rather than silently producing an empty header.
RUN mkdir -p internal/ebpf/headers && \
    bpftool btf dump file /sys/kernel/btf/vmlinux format c > internal/ebpf/headers/vmlinux.h
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags ebpf -trimpath -ldflags="-s -w" -o /out/nodelocalproxy .

FROM scratch

COPY --from=build --chmod=755 /out/nodelocalproxy /nodelocalproxy

USER 65532:65532

EXPOSE 16443 16444

ENTRYPOINT ["/nodelocalproxy"]
