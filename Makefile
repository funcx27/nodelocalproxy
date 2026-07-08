.PHONY: build test lint fmt vet builder docker-build docker-push clean release-tarballs

BINARY := nodelocalproxy
VERSION ?= dev
DIST := dist
IMAGE ?= $(BINARY):$(VERSION)
PLATFORMS ?= linux/amd64,linux/arm64
BUILDER ?= mybuilder
PROXY ?= http://127.0.0.1:10808
COMPRESSION ?= zstd

export http_proxy := $(PROXY)
export https_proxy := $(PROXY)
export HTTP_PROXY := $(PROXY)
export HTTPS_PROXY := $(PROXY)

# Static, stripped build: CGO_ENABLED=0 guarantees no glibc dependency, so the
# single binary runs on any Linux without a compatible libc. -trimpath/-s/-w
# trim build metadata for a smaller, reproducible artifact.
LDFLAGS := -s -w
GOFLAGS := -trimpath

# build the host-arch binary into bin/ (fast, debug-friendly).
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o bin/$(BINARY) .

# test runs the unit tests with the race detector to catch pool/proxy races.
# The race detector requires cgo, so test is exempt from the CGO_ENABLED=0 used
# for the production builds — the shipped binary stays fully static (see build).
test:
	go test -race -count=1 ./...

lint:
	golangci-lint run --timeout=5m

vet:
	go vet ./...

fmt:
	gofmt -w .

builder:
	docker buildx inspect $(BUILDER) >/dev/null 2>&1 || docker buildx create --name $(BUILDER) --driver docker-container --driver-opt env.http_proxy=$(http_proxy) --driver-opt env.https_proxy=$(https_proxy) --use
	docker buildx inspect --bootstrap $(BUILDER)

docker-build:
	docker buildx build \
		--builder $(BUILDER) \
		--load \
		--build-arg GO_VERSION=1.25.5 \
		--build-arg HTTP_PROXY=$(HTTP_PROXY) \
		--build-arg HTTPS_PROXY=$(HTTPS_PROXY) \
		--build-arg http_proxy=$(http_proxy) \
		--build-arg https_proxy=$(https_proxy) \
		-t $(IMAGE) .

docker-push:
	docker buildx build \
		--builder $(BUILDER) \
		--platform $(PLATFORMS) \
		--output type=image,compression=$(COMPRESSION) \
		--build-arg GO_VERSION=1.25.5 \
		--build-arg HTTP_PROXY=$(HTTP_PROXY) \
		--build-arg HTTPS_PROXY=$(HTTPS_PROXY) \
		--build-arg http_proxy=$(http_proxy) \
		--build-arg https_proxy=$(https_proxy) \
		-t $(IMAGE) \
		--push .

# release-tarballs cross-compiles static binaries for the supported arches and
# packs each into a versioned tarball under dist/. The release workflow calls
# this and uploads the tarballs to the GitHub Release.
release-tarballs:
	@mkdir -p $(DIST)
	@for arch in amd64 arm64; do \
		echo ">> building $(BINARY) $(VERSION) linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch \
			go build $(GOFLAGS) -ldflags="$(LDFLAGS)" \
			-o $(DIST)/$(BINARY)_$(VERSION)_linux_$$arch . || exit 1; \
		tar -C $(DIST) -czf $(DIST)/$(BINARY)_$(VERSION)_linux_$$arch.tar.gz \
			$(BINARY)_$(VERSION)_linux_$$arch || exit 1; \
		rm -f $(DIST)/$(BINARY)_$(VERSION)_linux_$$arch; \
	done
	@echo ">> tarballs in $(DIST)/:" && ls -lh $(DIST)/*.tar.gz

clean:
	rm -rf bin/ $(DIST)/
