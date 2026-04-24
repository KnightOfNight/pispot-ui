# syntax=docker/dockerfile:1.7

# ---- build stage ---------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Build identity, supplied by `make docker-build` (which in turn reads
# git state on the host). Defaults to "unknown"/"false" so ad-hoc
# `docker build .` invocations still produce a runnable binary.
ARG COMMIT=unknown
ARG DIRTY=false
ARG BUILD_TIME=unknown

# Copy module files first to leverage layer caching.
COPY go.mod ./
# go.sum copied if/when dependencies are added.
COPY go.su[m] ./

# Pre-download modules (no-op until we add deps, but keeps the layer stable).
RUN go mod download

# Copy the rest of the source (including embedded web assets).
COPY . .

# Static, trimmed, stripped binary. Build identity is injected via
# -ldflags -X into internal/buildinfo so the running instance reports
# the exact commit it was built from.
ENV CGO_ENABLED=0 \
    GOOS=linux
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/mcs-net/pispot-ui/internal/buildinfo.Commit=${COMMIT} \
        -X github.com/mcs-net/pispot-ui/internal/buildinfo.Dirty=${DIRTY} \
        -X github.com/mcs-net/pispot-ui/internal/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/pispot-ui ./cmd/pispot-ui

# ---- runtime stage -------------------------------------------------------
FROM alpine:3.20

# Tools required at runtime:
#   iw       — station dump, WAN link info
#   iproute2 — `ip -j` for addresses and routes
#   ca-certificates, tzdata — hygiene
RUN apk add --no-cache iw iproute2 ca-certificates tzdata

COPY --from=build /out/pispot-ui /usr/local/bin/pispot-ui

EXPOSE 8080

# Default env values; overridden by docker-compose.yml on the Pi.
ENV LISTEN_ADDR=":8080" \
    HOTSPOT_IF="wlan0" \
    WAN_IF="wlan1" \
    ADMIN_IF="eth0" \
    PROC_PATH="/host/proc" \
    SYS_PATH="/host/sys" \
    LEASES_PATH="/host/dnsmasq.leases"

ENTRYPOINT ["/usr/local/bin/pispot-ui"]
