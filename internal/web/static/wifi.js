// pispot-ui — WiFi network management page
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  function humanizeError(s) {
    if (!s) return "";
    return s.charAt(0).toUpperCase() + s.slice(1);
  }

  function setError(id, msg) {
    const el = $(id);
    if (!el) return;
    el.textContent = msg ? humanizeError(msg) : "";
  }

  // --- state ---------------------------------------------------------------

  // supplicantActive: reflects wan.supplicant_active from /api/stats.
  // The Restart WPA Supplicant button is enabled only when wpa_supplicant
  // is running — if it is stopped the new config will be used automatically
  // on the next WAN Up, so there is nothing to restart.
  let supplicantActive = false;

  // reloadInProgress: true while a restart op is in-flight (including the
  // Restarting… and Restarted display periods). Prevents the fetchStats
  // poll from re-enabling the button mid-display.
  let reloadInProgress = false;

  function updateReloadButton() {
    const btn = $("btn-reload");
    if (!btn) return;
    if (reloadInProgress) return; // don't touch button during restart
    btn.disabled = !supplicantActive;
  }

  // --- footer (matches dashboard) ------------------------------------------

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

  function renderFooter(meta) {
    if (!meta) return;
    $("meta-host").textContent      = meta.hostname || "-";
    $("meta-uptime").textContent    = fmtDuration(meta.uptime_seconds);
    $("meta-commit").textContent    = meta.commit || "-";
    $("meta-buildtime").textContent = meta.build_time || "-";
    $("meta-dirty").hidden          = !meta.dirty;
  }

  // --- stats fetch (wan state + footer) ------------------------------------

  async function fetchStats() {
    try {
      const r = await fetch("/api/stats", { cache: "no-store", credentials: "include" });
      if (!r.ok) return;
      const data = await r.json();
      supplicantActive = !!(data.wan && data.wan.supplicant_active);
      renderFooter(data.meta || {});
      updateReloadButton();
    } catch (_) {
      // Non-fatal: footer stays at defaults, button stays disabled.
    }
  }

  // --- network list --------------------------------------------------------

  async function loadNetworks() {
    setError("wifi-error", "");
    try {
      const r = await fetch("/api/wifi/networks", { cache: "no-store", credentials: "include" });
      if (r.status === 403) {
        renderError("Admin access required.");
        return;
      }
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        renderError(b.error || `Error ${r.status}`);
        return;
      }
      const data = await r.json();
      renderNetworks(data.networks || []);
    } catch (e) {
      renderError(`Failed to load networks: ${e.message}`);
    }
  }

  function renderError(msg) {
    setError("wifi-error", msg);
    $("wifi-rows").innerHTML = `<tr><td colspan="4">—</td></tr>`;
  }

  function renderNetworks(networks) {
    const tbody = $("wifi-rows");
    if (!networks || networks.length === 0) {
      tbody.innerHTML = `<tr><td colspan="4">No networks configured</td></tr>`;
      return;
    }
    const isLast = networks.length === 1;
    tbody.innerHTML = networks.map((n, i) => `
      <tr>
        <td class="num">${i + 1}</td>
        <td class="mono">${escHtml(n.ssid)}</td>
        <td class="mono wifi-psk">••••••••</td>
        <td><button class="ctrl-btn btn-remove"
            data-ssid="${escAttr(n.ssid)}"
            ${isLast ? "disabled title=\"Cannot remove the last network\"" : ""}
            >Remove</button></td>
      </tr>`).join("");

    tbody.querySelectorAll(".btn-remove").forEach((btn) => {
      btn.addEventListener("click", () => {
        const ssid = btn.dataset.ssid;
        if (!window.confirm(`Remove network "${ssid}"?`)) return;
        removeNetwork(ssid);
      });
    });
  }

  // --- add network ---------------------------------------------------------

  async function addNetwork() {
    setError("add-error", "");
    const ssid = $("add-ssid").value.trim();
    const psk  = $("add-password").value;
    if (!ssid) { setError("add-error", "SSID is required"); return; }
    if (!psk)  { setError("add-error", "Password is required"); return; }
    if (psk.length < 8 || psk.length > 63) {
      setError("add-error", `Password must be 8–63 characters (got ${psk.length})`);
      return;
    }

    $("btn-add").disabled = true;
    try {
      const r = await fetch("/api/wifi/networks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ssid, psk }),
        cache: "no-store",
        credentials: "include",
      });
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        setError("add-error", b.error || `Error ${r.status}`);
        return;
      }
      $("add-ssid").value     = "";
      $("add-password").value = "";
      await loadNetworks();
    } catch (e) {
      setError("add-error", e.message);
    } finally {
      $("btn-add").disabled = false;
    }
  }

  // --- remove network ------------------------------------------------------

  async function removeNetwork(ssid) {
    setError("wifi-error", "");
    try {
      const r = await fetch(`/api/wifi/networks/${encodeURIComponent(ssid)}`, {
        method: "DELETE",
        cache: "no-store",
        credentials: "include",
      });
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        setError("wifi-error", b.error || `Error ${r.status}`);
        return;
      }
      await loadNetworks();
    } catch (e) {
      setError("wifi-error", e.message);
    }
  }

  // --- restart wpa supplicant -----------------------------------------------

  async function reloadSupplicant() {
    setError("reload-error", "");
    const btn = $("btn-reload");
    reloadInProgress = true;
    btn.disabled    = true;
    btn.textContent = "Restarting…";

    // Minimum 5s display of "Restarting…" so the operator can see it
    // even when the API returns instantly.
    const minDelay = new Promise((resolve) => setTimeout(resolve, 5000));

    try {
      const [r] = await Promise.all([
        fetch("/api/wifi/reload", {
          method: "POST",
          cache: "no-store",
          credentials: "include",
        }),
        minDelay,
      ]);
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        setError("reload-error", "Restart failed — check system logs for details");
        // On error: skip "Restarted" phase, reset immediately.
        reloadInProgress = false;
        btn.textContent = "Restart WPA Supplicant";
        await fetchStats();
        return;
      }
      // Show "Restarted" for 5 seconds before returning control.
      btn.textContent = "Restarted";
      await new Promise((resolve) => setTimeout(resolve, 5000));
    } catch (e) {
      setError("reload-error", e.message);
    } finally {
      reloadInProgress = false;
      btn.textContent = "Restart WPA Supplicant";
      await fetchStats();
    }
  }

  // --- helpers -------------------------------------------------------------

  function escHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function escAttr(s) {
    return escHtml(s);
  }

  // --- init ----------------------------------------------------------------

  function init() {
    fetchStats();
    loadNetworks();
    // Poll /api/stats every 3s to keep the Reload button state current.
    // e.g. after a reload cycle, re-enables the button when wpa_supplicant
    // comes back up. Only updates button state and footer — does not
    // touch the network list or form fields, so form entry is unaffected.
    setInterval(fetchStats, 3000);
    $("btn-add").addEventListener("click", addNetwork);
    $("btn-reload").addEventListener("click", reloadSupplicant);
    [$("add-ssid"), $("add-password")].forEach((el) => {
      el.addEventListener("keydown", (e) => {
        if (e.key === "Enter") addNetwork();
      });
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
