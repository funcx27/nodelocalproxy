# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25.5

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build

WORKDIR /src

ARG TARGETOS
ARG TARGETARCH
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG http_proxy
ARG https_proxy

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
	go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath -ldflags="-s -w" -o /out/nodelocalproxy .

FROM scratch

COPY --from=build --chmod=755 /out/nodelocalproxy /nodelocalproxy

USER 65532:65532
EXPOSE 16443 16444
ENTRYPOINT ["/nodelocalproxy"]
