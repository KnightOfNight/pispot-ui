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
    tbody.innerHTML = rows || `<tr><td colspan="6" class="error">no interfaces</td></tr>`;
  }

  function renderHotspot(h) {
    $("hotspot-iface").textContent = h.interface ? `(${h.interface})` : "";
    const countEl = $("hotspot-count");
    countEl.textContent = h.client_count;
    // Count renders in the default .num color; no threshold coloring.
    const tbody = $("hotspot-rows");
    if (!h.clients || h.clients.length === 0) {
      tbody.innerHTML = `<tr><td colspan="7" class="sub">no clients</td></tr>`;
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
    $("hotspot-error").textContent = h.error || "";
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
    $("wan-error").textContent   = w.error || "";
  }

  function renderAdmin(a) {
    $("admin-iface").textContent = a.interface ? `(${a.interface})` : "";
    const linkEl = $("admin-link");
    linkEl.textContent = a.link ? "up" : "down";
    setClass(linkEl, a.link ? "ok" : null); // down on admin is not "bad" — neutral
    $("admin-ip").textContent = a.ip || "-";
    // Gateway on eth0 is expected to always be empty on this Pi: eth0
    // is administration-only and must not carry a default route. A
    // non-empty value indicates a routing-table anomaly and is flagged
    // in red with a hover hint.
    const gwEl = $("admin-gw");
    gwEl.textContent = a.gateway || "-";
    setClass(gwEl, a.gateway ? "bad" : null);
    gwEl.title = a.gateway
      ? "eth0 should never have a default route; investigate"
      : "";
    $("admin-error").textContent = a.error || "";
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

    $("sys-error").textContent = sys.error || "";
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
  }

  function render(data) {
    renderInterfaces(data.interfaces || {});
    renderHotspot(data.hotspot || {});
    renderWAN(data.wan || {});
    renderAdmin(data.admin || {});
    renderSystem(data.system || {});
    renderMeta(data.meta || {});
  }

  // --- refresh loop -------------------------------------------------------

  let timer = null;
  let currentInterval = DEFAULT_INTERVAL;

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

    setInterval_(chosen);
    tick(); // immediate first fetch
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
