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

.PHONY: all build test tidy vet fmt run-local docker-build image-size clean help

all: build

help:
	@echo "Targets:"
	@echo "  build         Compile the Go binary locally (Mac-native) for syntax checks"
	@echo "  test          Run unit tests"
	@echo "  vet           go vet"
	@echo "  fmt           gofmt -w on all Go files"
	@echo "  tidy          go mod tidy (keep go directive at 1.26)"
	@echo "  run-local     Build and run locally; API returns stub data"
	@echo "  docker-build  Build the container image locally (linux/arm64)"
	@echo "  image-size    Print the size of the local image"
	@echo "  clean         Remove local build artifacts"

build:
	go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) $(PKG)

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
	docker build --platform $(PLATFORM) -t $(IMAGE) .

image-size:
	@docker image inspect $(IMAGE) --format '{{.Size}}' 2>/dev/null \
		| awk '{printf "%.1f MB\n", $$1/1024/1024}' \
		|| echo "image not built"

clean:
	rm -rf bin
