# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25.5

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build

# clang/llvm/kernel headers + bpftool compile/generate the eBPF program
# (vmlinux.h from BTF) under the ebpf build tag during `go build`.
RUN apt-get update && apt-get install -y --no-install-recommends \
    clang llvm linux-libc-dev linux-tools-generic bpftool \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

ARG TARGETOS TARGETARCH HTTP_PROXY HTTPS_PROXY http_proxy https_proxy

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
