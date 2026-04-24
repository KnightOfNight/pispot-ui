# pispot-ui

Read-only web dashboard for the Pi 5 hotspot/router (branded **N1QZS Radio
Hotspot**). Shows per-interface throughput, hotspot clients, and WAN SSID
info. Lightweight Go binary served from a small Alpine container with host
networking and read-only access to `/proc`, `/sys`, and the dnsmasq leases
file.

pispot &copy; MCS 'Net Productions 2026

## Status

**M1 — scaffold.** The API returns stub data so the frontend contract and
Docker packaging can be validated end-to-end before live collectors are
wired in. Subsequent milestones (M2–M5) add throughput, hotspot client
enumeration, WAN SSID, and polish.

## Architecture

```
browser (LAN) ──► http://<pi>:8080 ──► pispot-ui container (host netns)
                                        │
                                        ├─ /proc (ro)              [throughput]
                                        ├─ /sys  (ro)              [link/carrier]
                                        ├─ iw dev wlan0 station dump [clients]
                                        ├─ iw dev wlan1 link        [WAN SSID]
                                        └─ dnsmasq.leases (ro)      [hostnames]
```

- Backend: Go 1.26, static binary, embedded static assets.
- Frontend: vanilla HTML/CSS/JS. Auto-refresh via `fetch` polling.
  Default 3 s; dropdown offers 1/2/3/5/10/off; choice persisted in
  `pispot_interval` cookie.
- Runtime: Alpine 3.20 + `iw` + `iproute2`.
- Container: `--network host --pid host`, `cap_drop ALL`, `cap_add NET_ADMIN`,
  read-only rootfs, ro bind-mounts.

## Layout

```
cmd/pispot-ui/       main entrypoint
internal/config      env-driven configuration
internal/api         HTTP handlers + JSON schema
internal/web         embedded static assets
internal/netstats    (M2) /proc/net/dev throughput collector
internal/hotspot     (M3) iw + dnsmasq lease enrichment
internal/wan         (M4) iw link + ip addr/route
internal/admin       (M4) eth0 link/IP
```

## Local development (Mac)

```
make build          # compile sanity check
make vet            # go vet
make test           # unit tests (added with M2+)
make docker-build   # build the linux/arm64 image locally
make run-local      # run the binary on the Mac; API returns stub data
```

`run-local` serves `http://localhost:8080/` using stub JSON — useful for
iterating on the frontend without the Pi. Live system data requires the
container on the Pi (host networking + /proc access).

## Deployment (Pi)

Git-based workflow. You push from the Mac, pull on the Pi, and rebuild
the container in place.

On the Pi, for a first-time clone:

```
git clone <repo-url> ~/pispot-ui
cd ~/pispot-ui
docker compose up -d --build
```

Subsequent updates:

```
cd ~/pispot-ui
git pull
docker compose up -d --build
```

Verify:

```
curl -s http://localhost:8080/healthz     # -> ok
curl -s http://localhost:8080/api/stats | head
```

Then browse to `http://<pi-host>:8080/` on the LAN.

## Configuration

All via environment variables (see `.env.example`). Defaults in
`docker-compose.yml` match the target Pi layout:

| Variable      | Default                         | Purpose                              |
|---------------|---------------------------------|--------------------------------------|
| `LISTEN_ADDR` | `:8080`                         | HTTP listen address                  |
| `HOTSPOT_IF`  | `wlan0`                         | LAN / hotspot interface              |
| `WAN_IF`      | `wlan1`                         | Upstream / roaming WAN interface     |
| `ADMIN_IF`    | `eth0`                          | Administration interface             |
| `PROC_PATH`   | `/host/proc`                    | Path to host `/proc` in container    |
| `SYS_PATH`    | `/host/sys`                     | Path to host `/sys` in container     |
| `LEASES_PATH` | `/host/dnsmasq.leases`          | dnsmasq leases file                  |

## Endpoints

| Method | Path          | Description                 |
|--------|---------------|-----------------------------|
| GET    | `/`           | Dashboard (HTML)            |
| GET    | `/style.css`  | Stylesheet                  |
| GET    | `/app.js`     | Client script               |
| GET    | `/api/stats`  | JSON stats snapshot         |
| GET    | `/healthz`    | Liveness — plain `ok`       |

## Security notes

- No authentication. `:8080` is exposed on every interface Docker sees
  (wlan0, wlan1, eth0). Restrict at the host firewall if needed.
- Data exposed: interface names/counters, hotspot client MACs/IPs/hostnames,
  WAN SSID/BSSID/signal. Read-only.
- Container runs with `cap_drop: [ALL]` + `cap_add: [NET_ADMIN]` and a
  read-only rootfs.

## Go toolchain

The `go` directive in `go.mod` is pinned to `1.26`. The build stage uses
`golang:1.26-alpine`. Do not bump these independently; keep Mac and
container versions in lock step.
