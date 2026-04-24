# pispot-ui — local development Makefile
#
# These targets run on the Mac for compile sanity and local Docker builds.
# Deployment to the Pi is handled via your normal git workflow and is not
# scripted here.

BINARY     := pispot-ui
PKG        := ./cmd/pispot-ui
IMAGE      := pispot-ui:latest

# The Pi builds a linux/arm64 image; on an arm64 Mac this requires no
# emulation, but we set --platform explicitly so local and Pi builds match.
PLATFORM   := linux/arm64

# Build identity injected via -ldflags -X into internal/buildinfo. These
# default to "unknown" when git is unavailable (shallow clones, tarball
# extractions, etc.) so the resulting binary is still runnable.
#
# DIRTY uses git status --porcelain (strict): any untracked, modified, or
# staged change flips the flag. BUILD_TIME is RFC3339 UTC.
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIRTY      := $(shell test -z "$$(git status --porcelain 2>/dev/null)" && echo false || echo true)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

BUILDINFO_PKG := github.com/mcs-net/pispot-ui/internal/buildinfo
LDFLAGS       := -s -w \
                 -X $(BUILDINFO_PKG).Commit=$(COMMIT) \
                 -X $(BUILDINFO_PKG).Dirty=$(DIRTY) \
                 -X $(BUILDINFO_PKG).BuildTime=$(BUILD_TIME)

# Pi SSH target for `make ship`. Override on the command line if needed:
#   make ship PI_HOST=other-host.local
PI_HOST    := n1qzs-radios.local

.PHONY: all build test tidy vet fmt run-local docker-build image-size \
        ship build-and-ship engage deploy clean help

all: build

help:
	@echo "Targets:"
	@echo "  build         Compile the Go binary locally (Mac-native) for syntax checks"
	@echo "  test          Run unit tests"
	@echo "  vet           go vet"
	@echo "  fmt           gofmt -w on all Go files"
	@echo "  tidy          go mod tidy (keep go directive at 1.26)"
	@echo "  run-local     Build and run locally; collectors will fail on non-Linux hosts"
	@echo "  docker-build  Build the container image locally (linux/arm64)"
	@echo "  image-size    Print the size of the local image"
	@echo "  ship          Save local image and load it on the Pi ($(PI_HOST))"
	@echo "  build-and-ship  docker-build then ship"
	@echo "  engage        SSH to Pi and (re)start the container from the shipped image"
	@echo "  deploy        docker-build, ship, engage (full Mac->Pi update in one shot)"
	@echo "  clean         Remove local build artifacts"

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

run-local: build
	LISTEN_ADDR=":8080" \
	PROC_PATH="/proc" \
	SYS_PATH="/sys" \
	LEASES_PATH="/tmp/dnsmasq.leases.stub" \
	./bin/$(BINARY)

docker-build:
	docker build --platform $(PLATFORM) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DIRTY=$(DIRTY) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(IMAGE) .

image-size:
	@docker image inspect $(IMAGE) --format '{{.Size}}' 2>/dev/null \
		| awk '{printf "%.1f MB\n", $$1/1024/1024}' \
		|| echo "image not built"

# Ship the locally-built image to the Pi over SSH.
# Assumes the image $(IMAGE) already exists locally; run `make docker-build`
# first (or use `make build-and-ship`). On the Pi, use:
#   docker compose up -d --no-build
ship:
	docker save $(IMAGE) | gzip | ssh $(PI_HOST) 'gunzip | docker load'

# One-shot: rebuild the image and ship it to the Pi.
build-and-ship: docker-build ship

# Start or restart the container on the Pi using the locally-shipped image.
# Pipes docker-compose.yml over SSH so the Pi does not need the git repo or
# any local files beyond the Docker image loaded by `make ship`.
# Uses --project-name pispot-ui so compose reconciles with any existing
# container from an earlier git-clone-based deployment.
COMPOSE_FILE := docker-compose.yml
COMPOSE_PROJECT := pispot-ui

engage:
	ssh $(PI_HOST) 'docker compose --project-name $(COMPOSE_PROJECT) -f - up -d --no-build' < $(COMPOSE_FILE)

# Full Mac -> Pi update: build, ship, start/restart.
deploy: docker-build ship engage

clean:
	rm -rf bin
