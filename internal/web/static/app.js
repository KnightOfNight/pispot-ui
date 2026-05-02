// pispot-ui — dashboard refresh loop and DOM updates
(() => {
  "use strict";

  // Red/green thresholds. Tune here; may move to /api/config later.
  const THRESHOLDS = {
    wanSignal:     { warn: -60, bad: -75 },   // WAN dBm (less-negative = better)
    clientSignal:  { warn: -65, bad: -80 },   // client dBm
  };

  const DEFAULT_INTERVAL = 3;
  const COOKIE = "pispot_interval";

  // --- utilities ----------------------------------------------------------

  const $ = (id) => document.getElementById(id);

  function setCookie(k, v) {
    document.cookie = `${k}=${encodeURIComponent(v)}; path=/; max-age=31536000; SameSite=Lax`;
  }
  function getCookie(k) {
    const m = document.cookie.match(new RegExp("(?:^|; )" + k + "=([^;]*)"));
    return m ? decodeURIComponent(m[1]) : null;
  }

  function classifyHigh(v, t) {
    // Higher value is worse (throughput, client count).
    if (v >= t.bad) return "bad";
    if (v >= t.warn) return "warn";
    return "ok";
  }
  function classifyLow(v, t) {
    // Lower value is worse (signal dBm: -90 is worse than -50).
    if (v <= t.bad) return "bad";
    if (v <= t.warn) return "warn";
    return "ok";
  }

  function setClass(el, cls) {
    if (!el) return;
    el.classList.remove("ok", "warn", "bad");
    if (cls) el.classList.add(cls);
  }

  // humanizeError translates a backend error string into a UI-ready
  // uppercase message. Known prefixes get friendly labels; unmapped
  // strings are uppercased as-is.
  function humanizeError(s) {
    if (!s) return "";
    let out;
    if (s === "interface absent") {
      out = "Interface not found";
    } else if (s.startsWith("iw: ")) {
      out = "Wireless tool error: " + s.slice(4);
    } else if (s.startsWith("ip addr: ")) {
      out = "Address lookup failed: " + s.slice(9);
    } else if (s.startsWith("ip route: ")) {
      out = "Route lookup failed: " + s.slice(10);
    } else if (s.startsWith("leases: ")) {
      out = "DHCP leases unreadable: " + s.slice(8);
    } else {
      out = s;
    }
    return out.toUpperCase();
  }

  function fmtBytes(n) {
    if (!Number.isFinite(n)) return "-";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(n >= 10 || i === 0 ? 0 : 1) + " " + u[i];
  }

  function fmtDuration(sec) {
    if (!Number.isFinite(sec) || sec < 0) return "-";
    sec = Math.floor(sec);
    const d = Math.floor(sec / 86400); sec -= d * 86400;
    const h = Math.floor(sec / 3600);  sec -= h * 3600;
    const m = Math.floor(sec / 60);    sec -= m * 60;
    if (d) return `${d}d ${h}h`;
    if (h) return `${h}h ${m}m`;
    if (m) return `${m}m ${sec}s`;
    return `${sec}s`;
  }

  function fmtMbps(v) {
    if (!Number.isFinite(v)) return "-";
    if (v >= 100) return v.toFixed(0);
    if (v >= 10)  return v.toFixed(1);
    return v.toFixed(2);
  }

  // --- rendering ----------------------------------------------------------

  function renderInterfaces(interfaces) {
    const tbody = $("iface-rows");
    const keys = Object.keys(interfaces);
    const rows = keys.map((name) => {
      const i = interfaces[name];
      const upCls = i.up ? "ok" : "bad";
      return `<tr>
        <td class="mono">${name}</td>
        <td class="num ${upCls}">${i.up ? "up" : "down"}</td>
        <td class="num">${fmtMbps(i.rx_mbps)}</td>
        <td class="num">${fmtMbps(i.tx_mbps)}</td>
        <td class="num">${fmtBytes(i.rx_total_bytes)}</td>
        <td class="num">${fmtBytes(i.tx_total_bytes)}</td>
      </tr>`;
    }).join("");
    tbody.innerHTML = rows || `<tr><td colspan="6">No interfaces</td></tr>`;
  }

  function renderHotspot(h) {
    $("hotspot-iface").textContent = h.interface ? `(${h.interface})` : "";
    const countEl = $("hotspot-count");
    countEl.textContent = h.client_count;
    // Count renders in the default .num color; no threshold coloring.
    const tbody = $("hotspot-rows");
    if (!h.clients || h.clients.length === 0) {
      tbody.innerHTML = `<tr><td colspan="7">No clients</td></tr>`;
    } else {
      tbody.innerHTML = h.clients.map((c) => {
        // Some Wi-Fi drivers (notably brcmfmac on Pi 5 built-in wireless
        // in AP mode) do not report per-station signal in the station
        // dump. The server emits 0 in that case; render it as N/A so
        // the cell reads as "unavailable" rather than a bogus value.
        const hasSignal = c.signal_dbm !== 0;
        const sigCls = hasSignal ? classifyLow(c.signal_dbm, THRESHOLDS.clientSignal) : "";
        const sigText = hasSignal ? c.signal_dbm : "N/A";
        return `<tr>
          <td class="mono">${c.mac || "-"}</td>
          <td class="mono">${c.ip || "-"}</td>
          <td>${c.hostname || "-"}</td>
          <td class="num ${sigCls}">${sigText}</td>
          <td class="num">${fmtDuration(c.connected_seconds)}</td>
          <td class="num">${fmtBytes(c.rx_bytes)}</td>
          <td class="num">${fmtBytes(c.tx_bytes)}</td>
        </tr>`;
      }).join("");
    }
    $("hotspot-error").textContent = humanizeError(h.error);
  }

  function renderWAN(w) {
    $("wan-iface").textContent = w.interface ? `(${w.interface})` : "";
    const connEl = $("wan-connected");
    connEl.textContent = w.connected ? "yes" : "no";
    setClass(connEl, w.connected ? "ok" : "bad");

    $("wan-ssid").textContent = w.ssid || "-";
    $("wan-bssid").textContent = w.bssid || "-";

    const sigEl = $("wan-signal");
    if (w.connected && Number.isFinite(w.signal_dbm)) {
      sigEl.textContent = `${w.signal_dbm} dBm`;
      setClass(sigEl, classifyLow(w.signal_dbm, THRESHOLDS.wanSignal));
    } else {
      sigEl.textContent = "-";
      setClass(sigEl, null);
    }

    $("wan-freq").textContent    = w.freq_mhz ? `${w.freq_mhz} MHz` : "-";
    $("wan-bitrate").textContent = w.tx_bitrate_mbps ? `${w.tx_bitrate_mbps} Mbps` : "-";
    $("wan-ip").textContent      = w.ip || "-";
    $("wan-gw").textContent      = w.gateway || "-";
    $("wan-error").textContent   = humanizeError(w.error);

    // WAN controls: only shown to admin when interface is physically present.
    // Buttons are never shown when interface_present is false — there is
    // nothing to control if the hardware isn't there.
    // Config button: always visible to admin regardless of interface state.
    const configBtn = $("btn-wifi-config");
    if (configBtn) configBtn.hidden = (currentRole !== "admin");

    // WAN Up/Down buttons: visible to admin only when interface is present.
    const upBtn   = $("btn-wan-up");
    const downBtn = $("btn-wan-down");
    if (upBtn && downBtn) {
      const showWanBtns = currentRole === "admin" && w.interface_present;
      upBtn.hidden   = !showWanBtns;
      downBtn.hidden = !showWanBtns;
      if (showWanBtns) {
        if (wanPending === "up" && !w.connected) {
          upBtn.disabled   = true;  upBtn.title   = "WAN starting…";
          downBtn.disabled = true;  downBtn.title = "WAN starting…";
        } else if (wanPending === "down" && w.connected) {
          downBtn.disabled = true;  downBtn.title = "WAN stopping…";
          upBtn.disabled   = true;  upBtn.title   = "WAN stopping…";
        } else {
          if (wanPending !== null) {
            const statusEl = $("wan-op-status");
            if (statusEl) {
              statusEl.textContent = wanPending === "up" ? "WAN up" : "WAN down";
              setTimeout(() => {
                if (statusEl) { statusEl.textContent = ""; statusEl.className = "wan-op-status"; }
              }, 3000);
            }
            wanPending = null;
          }
          upBtn.disabled   = w.connected;
          downBtn.disabled = !w.connected;
          upBtn.title   = w.connected  ? "WAN is already up"   : "Start WAN connection";
          downBtn.title = !w.connected ? "WAN is already down" : "Stop WAN connection";
        }
      }
    }
  }

  function renderAdmin(a) {
    $("admin-iface").textContent = a.interface ? `(${a.interface})` : "";
    const linkEl = $("admin-link");
    linkEl.textContent = a.link ? "up" : "down";
    setClass(linkEl, a.link ? "ok" : null); // down on admin is not "bad" — neutral
    $("admin-ip").textContent = a.ip || "-";
    // eth0 is dual-purpose (Admin / Backup WAN). It legitimately holds a
    // default route when wlan1 is down and DHCP supplied a gateway, so
    // the Gateway field is rendered as a normal value with no warning.
    $("admin-gw").textContent = a.gateway || "-";
    $("admin-error").textContent = humanizeError(a.error);
  }

  function renderSystem(sys) {
    // Load averages — all three, stacked vertically so the labels and
    // numbers line up in two columns inside the kv-grid value cell.
    // Uses innerHTML because the cell is a self-contained subgrid; the
    // source values come from JSON and are clamped by fmtLoad.
    const has = (v) => Number.isFinite(v);
    const loadEl = $("sys-load");
    if (has(sys.load_1m) || has(sys.load_5m) || has(sys.load_15m)) {
      loadEl.innerHTML =
        `<span class="k">1m</span><span>${fmtLoad(sys.load_1m)}</span>` +
        `<span class="k">5m</span><span>${fmtLoad(sys.load_5m)}</span>` +
        `<span class="k">15m</span><span>${fmtLoad(sys.load_15m)}</span>`;
    } else {
      loadEl.textContent = "-";
    }

    // Memory: just used / total, no percentage, no "available".
    if (sys.mem_total_bytes) {
      $("sys-mem").textContent =
        `${fmtBytes(sys.mem_used_bytes)} / ${fmtBytes(sys.mem_total_bytes)}`;
    } else {
      $("sys-mem").textContent = "-";
    }

    // Temperature: one decimal.
    if (has(sys.temp_celsius) && sys.temp_celsius !== 0) {
      $("sys-temp").textContent = `${sys.temp_celsius.toFixed(1)} °C`;
    } else {
      $("sys-temp").textContent = "-";
    }

    // Throttle: binary; label as inferred since we derive from
    // temperature, not the firmware flag.
    const thEl = $("sys-throttle");
    if (sys.throttled) {
      thEl.textContent = "yes (inferred)";
      setClass(thEl, "bad");
    } else {
      thEl.textContent = "no";
      setClass(thEl, null);
    }

    $("sys-error").textContent = humanizeError(sys.error);
  }

  function fmtLoad(v) {
    if (!Number.isFinite(v)) return "-";
    return v.toFixed(2);
  }

  function renderMeta(m) {
    $("meta-host").textContent      = m.hostname || "-";
    $("meta-uptime").textContent    = fmtDuration(m.uptime_seconds);
    $("meta-commit").textContent    = m.commit || "-";
    $("meta-buildtime").textContent = m.build_time || "-";
    $("meta-dirty").hidden          = !m.dirty;
    // Track role globally so renderWAN can use it without receiving it
    // as a parameter. meta is rendered last, so currentRole is already
    // set when renderWAN first fires on the next cycle.
    currentRole = m.role || "";
  }

  function render(data) {
    // Update role before rendering sections that depend on it.
    currentRole = (data.meta && data.meta.role) || "";
    renderInterfaces(data.interfaces || {});
    renderHotspot(data.hotspot || {});
    renderWAN(data.wan || {});
    renderAdmin(data.admin || {});
    renderSystem(data.system || {});
    renderMeta(data.meta || {});
  }

  // --- WAN control operations ---------------------------------------------

  // wanPending tracks an in-progress WAN op so renderWAN doesn't override
  // button state while the physical connection is still transitioning.
  // "up"   = WAN Up was sent, waiting for wan.connected to become true.
  // "down" = WAN Down was sent, waiting for wan.connected to become false.
  // null   = no pending op; renderWAN controls button state normally.
  let wanPending = null;

  async function wanOp(op) {
    const statusEl = $("wan-op-status");
    const upBtn    = $("btn-wan-up");
    const downBtn  = $("btn-wan-down");

    // Show working state.
    statusEl.textContent = "Working…";
    statusEl.className   = "wan-op-status";
    upBtn.disabled       = true;
    downBtn.disabled     = true;

    try {
      const r = await fetch(`/api/wan/${op}`, { method: "POST", cache: "no-store" });
      if (r.ok) {
        statusEl.textContent = op === "up" ? "WAN starting…" : "WAN stopping…";
        statusEl.className   = "wan-op-status ok";
        // Set pending state so renderWAN holds both buttons disabled while
        // the physical connection transitions. renderWAN clears wanPending
        // once wan.connected reaches the expected value.
        wanPending = op;
      } else {
        const body = await r.json().catch(() => ({}));
        statusEl.textContent = body.error || `Error (${r.status})`;
        statusEl.className   = "wan-op-status bad";
        // Op failed — restore buttons so operator can retry.
        upBtn.disabled   = false;
        downBtn.disabled = false;
      }
    } catch (e) {
      statusEl.textContent = `Error: ${e.message}`;
      statusEl.className   = "wan-op-status bad";
      upBtn.disabled   = false;
      downBtn.disabled = false;
    }

  }

  // --- refresh loop -------------------------------------------------------

  let timer = null;
  let currentInterval = DEFAULT_INTERVAL;
  let currentRole = "";

  function setStatus(state, title) {
    const el = $("status-label");
    if (!el) return;
    el.classList.remove("tag-ok", "tag-warn", "tag-bad");
    let text;
    switch (state) {
      case "ok":   text = "ONLINE";  el.classList.add("tag-ok");   break;
      case "bad":  text = "OFFLINE"; el.classList.add("tag-bad");  break;
      default:     text = "WAITING"; el.classList.add("tag-warn"); break;
    }
    el.textContent = text;
    el.title = title || "";
  }

  async function tick() {
    try {
      const r = await fetch("/api/stats", { cache: "no-store" });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const data = await r.json();
      render(data);
      setStatus("ok", "connected");
    } catch (e) {
      setStatus("bad", `error: ${e.message}`);
    }
  }

  function setInterval_(sec) {
    if (timer) { clearInterval(timer); timer = null; }
    currentInterval = sec;
    if (sec > 0) {
      timer = window.setInterval(tick, sec * 1000);
    }
  }

  function init() {
    const select = $("interval");
    const saved = parseInt(getCookie(COOKIE), 10);
    const chosen = Number.isFinite(saved) ? saved : DEFAULT_INTERVAL;
    select.value = String(chosen);

    select.addEventListener("change", () => {
      const v = parseInt(select.value, 10);
      setCookie(COOKIE, String(v));
      setInterval_(v);
    });

    // WAN control buttons — only visible to admin when interface is present;
    // rendered via renderWAN on each refresh cycle.
    const upBtn   = $("btn-wan-up");
    const downBtn = $("btn-wan-down");
    if (upBtn)   upBtn.addEventListener("click",   () => wanOp("up"));
    if (downBtn) downBtn.addEventListener("click", () => wanOp("down"));

    setInterval_(chosen);
    tick(); // immediate first fetch
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
