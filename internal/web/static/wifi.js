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

  // --- network list --------------------------------------------------------

  async function loadNetworks() {
    setError("wifi-error", "");
    try {
      const r = await fetch("/api/wifi/networks", { cache: "no-store" });
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
    tbody.innerHTML = networks.map((n, i) => `
      <tr>
        <td class="num">${i + 1}</td>
        <td class="mono">${escHtml(n.ssid)}</td>
        <td class="mono wifi-psk">${escHtml(n.psk)}</td>
        <td><button class="ctrl-btn btn-remove" data-ssid="${escAttr(n.ssid)}">Remove</button></td>
      </tr>`).join("");

    tbody.querySelectorAll(".btn-remove").forEach((btn) => {
      btn.addEventListener("click", () => removeNetwork(btn.dataset.ssid));
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

  // --- reload --------------------------------------------------------------

  async function reloadWAN() {
    setError("reload-error", "");
    $("btn-reload").disabled = true;
    $("btn-reload").textContent = "Reloading…";
    try {
      const r = await fetch("/api/wifi/reload", {
        method: "POST",
        cache: "no-store",
      });
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        setError("reload-error", b.error || `Error ${r.status}`);
      } else {
        $("btn-reload").textContent = "Reloaded";
        setTimeout(() => { $("btn-reload").textContent = "Reload WAN"; }, 3000);
      }
    } catch (e) {
      setError("reload-error", e.message);
    } finally {
      $("btn-reload").disabled = false;
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
    loadNetworks();
    $("btn-add").addEventListener("click", addNetwork);
    $("btn-reload").addEventListener("click", reloadWAN);
    // Allow submitting the add form with Enter in either field.
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
