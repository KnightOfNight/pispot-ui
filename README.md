# pispot-ui

Read-only web dashboard for the Pi 5 hotspot/router (branded **N1QZS Radio
Hotspot**). Shows per-interface throughput, hotspot clients, and WAN SSID
info. Lightweight Go binary served from a small Alpine container with host
networking and read-only access to `/proc`, `/sys`, and the dnsmasq leases
file.

pispot &copy; MCS 'Net Productions 2026

## Status

**M5 — System pane + bottom-row layout.** A new System section
reports load average (1/5/15 m), memory (used/total), SoC temperature
with thermal-zone auto-detection, and an inferred thermal-throttle
flag (temp ≥ 80 °C). WAN, Admin, and System now live side-by-side in
a single row of equal thirds below the Interfaces and Hotspot tables.

**M4 — all network sections live.** WAN link info (`iw dev <iface>
link` plus `ip -j addr` and `ip -j route`) and admin interface
(`operstate` plus `ip -j addr`) complete the dashboard. Every block in
`/api/stats` is sourced from the running system. WAN disconnection (no
associated AP) clears the IP/BSSID/SSID/signal/freq/bitrate/gateway
fields so the UI never shows stale data for a link that is
definitively not associated. All non-netstats collectors refresh
lazily with a 1 s TTL and 2 s exec timeout; on failure the last-good
data is retained and the error is surfaced in the section-level
`.error` field.

Hotspot-client signal note: Pi 5 built-in wireless (`brcmfmac` driver)
does not expose per-station RSSI in AP mode, so hotspot client signal
reads as `N/A` in the UI. A USB Wi-Fi adapter with a driver that
exposes signal via `iw station dump` would populate the column
automatically without further changes.

## UI notes

Visual signals used on the dashboard:

| Where              | State                        | Meaning                                                                                                                                           |
|--------------------|------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------|
| Interfaces Up      | green `up` / red `down`      | Kernel operstate for that interface.                                                                                                              |
| Hotspot Signal     | `N/A`                        | Driver does not expose per-station RSSI (Pi 5 built-in `brcmfmac` in AP mode).                                                                    |
| Hotspot Signal     | colored dBm                  | Client RSSI colored green / amber / red by the thresholds in `app.js` (only populated on drivers that report it).                                 |
| WAN Connected      | green `yes` / red `no`       | Whether the WAN interface is currently associated to an AP.                                                                                       |
| WAN Signal         | colored dBm                  | Upstream link RSSI colored by the thresholds in `app.js`.                                                                                         |
| Admin Link         | green `up` / neutral `down`  | eth0 operstate. eth0 is dual-purpose (Admin / Backup WAN); link-down is not an error.                                                             |
| System Throttled   | red `yes (inferred)`         | SoC temperature is at or above the 80 °C soft-throttle threshold. Inferred from sysfs temperature, not the firmware throttle flag (which requires `vcgencmd`). |
| Footer `dirty` tag | red tag                      | The running binary was built from a working tree with uncommitted or untracked changes.                                                           |

## Architecture

```
browser (LAN) ──► https://n1qzs-radios.private.magrathea.com/ ──► pispot-ui container (host netns)
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
make test           # unit tests
make docker-build   # build the linux/arm64 image locally
make run-local      # run the binary on the Mac; collectors fail on non-Linux
```

`run-local` serves `http://localhost:8080/`, but the collectors rely on
Linux `/proc`, `/sys`, `iw`, and `ip` which aren't present on macOS —
expect empty values and populated `.error` fields on every section.
Live system data requires the container on the Pi.

Local development uses HTTP unless `TLS_CERT_FILE` and `TLS_KEY_FILE`
are set. Production compose requires TLS and listens on `:443`.

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
curl -s https://n1qzs-radios.private.magrathea.com/healthz     # -> ok
curl -s https://n1qzs-radios.private.magrathea.com/api/stats | head
```

Then browse to `https://n1qzs-radios.private.magrathea.com/` on the LAN.

### Deploying a Mac-built image (no build on the Pi)

Building on the Pi currently fails because BuildKit's build network can't
reach the Alpine package CDN (IPv6/DNS quirk on the WAN uplink). Workaround
is to build on the Mac, load the image directly onto the Pi over SSH, and
start the container remotely without needing the git repo on the Pi.

One-shot from the Mac:

```
make deploy        # docker-build + ship + engage
```

Or in steps:

```
make docker-build       # build pispot-ui:latest locally (linux/arm64)
make ship               # save/gzip/ssh/docker-load onto the Pi
make engage             # ssh to Pi, pipe docker-compose.yml, up -d --no-build
```

`make ship` and `make engage` both target `PI_HOST=n1qzs-radios.local`.
Override on the command line if needed:

```
make deploy PI_HOST=some-other-host.local
```

`make engage` pipes `docker-compose.yml` over SSH and runs
`docker compose -f - --project-name pispot-ui up -d --no-build` on the
Pi, so the Pi needs nothing locally except the loaded Docker image — no
git checkout, no compose file on disk. The project name is pinned to
`pispot-ui` so compose reconciles with any container left over from an
earlier git-clone-based deployment.

Restoring a pure `git pull && docker compose up -d --build` workflow on
the Pi is deferred until the Pi-side build issue is resolved.

## Configuration

All via environment variables (see `.env.example`). Defaults in
`docker-compose.yml` match the target Pi layout:

| Variable      | Default                         | Purpose                              |
|---------------|---------------------------------|--------------------------------------|
| `LISTEN_ADDR` | `:443`                          | HTTPS listen address in production   |
| `TLS_CERT_FILE` | `/run/certs/fullchain.pem`     | TLS certificate/fullchain path       |
| `TLS_KEY_FILE` | `/run/certs/star_private_magrathea_com.key` | TLS private key path       |
| `REQUIRE_TLS` | `true`                          | Fail startup unless TLS is configured |
| `AUTH_SOCKET` | `/run/pispot-authd.sock`        | Path to pispot-authd Unix socket; empty = no auth (local dev) |
| `AUTH_REALM`  | `N1QZS Radio Hotspot`           | Browser Basic Auth realm string      |
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

- M6.2 adds Basic Auth via the host-side `pispot-authd` PAM helper.
  Access requires Unix group membership: `pispot-ro` (read-only) or
  `pispot-admin` (full admin). `/healthz` is unauthenticated.
  When `AUTH_SOCKET` is unset (local dev), auth is disabled.
- Production listens on `:443` on every interface Docker sees
  (wlan0, wlan1, eth0). Restrict at the host firewall if needed.
- Data exposed: interface names/counters, hotspot client MACs/IPs/hostnames,
  WAN SSID/BSSID/signal. Read-only.
- TLS certs are mounted from the host at runtime; private keys are not
  stored in this public repo or baked into the image.
- Container runs with `cap_drop: [ALL]` + `cap_add: [NET_ADMIN]` and a
  read-only rootfs.

## Go toolchain

The `go` directive in `go.mod` is pinned to `1.26`. The build stage uses
`golang:1.26-alpine`. Do not bump these independently; keep Mac and
container versions in lock step.
