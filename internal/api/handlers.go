// Package api provides HTTP handlers for the pispot-ui JSON API.
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mcs-net/pispot-ui/internal/admin"
	"github.com/mcs-net/pispot-ui/internal/authz"
	"github.com/mcs-net/pispot-ui/internal/buildinfo"
	"github.com/mcs-net/pispot-ui/internal/config"
	"github.com/mcs-net/pispot-ui/internal/hotspot"
	"github.com/mcs-net/pispot-ui/internal/netstats"
	"github.com/mcs-net/pispot-ui/internal/system"
	"github.com/mcs-net/pispot-ui/internal/wan"
)

// Interface holds per-interface throughput and totals.
type Interface struct {
	RxMbps       float64 `json:"rx_mbps"`
	TxMbps       float64 `json:"tx_mbps"`
	RxTotalBytes uint64  `json:"rx_total_bytes"`
	TxTotalBytes uint64  `json:"tx_total_bytes"`
	Up           bool    `json:"up"`
}

// HotspotClient describes one associated station on the LAN side.
type HotspotClient struct {
	MAC              string `json:"mac"`
	IP               string `json:"ip"`
	Hostname         string `json:"hostname"`
	SignalDBm        int    `json:"signal_dbm"`
	ConnectedSeconds uint64 `json:"connected_seconds"`
	RxBytes          uint64 `json:"rx_bytes"`
	TxBytes          uint64 `json:"tx_bytes"`
}

// Hotspot is the wlan0/LAN summary.
type Hotspot struct {
	Interface   string          `json:"interface"`
	ClientCount int             `json:"client_count"`
	Clients     []HotspotClient `json:"clients"`
	Error       string          `json:"error,omitempty"`
}

// WAN is the wlan1 (upstream) summary.
type WAN struct {
	Interface        string  `json:"interface"`
	InterfacePresent bool    `json:"interface_present"`
	Connected        bool    `json:"connected"`
	SSID             string  `json:"ssid"`
	BSSID            string  `json:"bssid"`
	SignalDBm        int     `json:"signal_dbm"`
	FreqMHz          int     `json:"freq_mhz"`
	TxBitrateMbps    float64 `json:"tx_bitrate_mbps"`
	IP               string  `json:"ip"`
	Gateway          string  `json:"gateway"`
	Error            string  `json:"error,omitempty"`
}

// Admin is the eth0 (administration) summary.
type Admin struct {
	Interface string `json:"interface"`
	IP        string `json:"ip"`
	Gateway   string `json:"gateway"`
	Link      bool   `json:"link"`
	Error     string `json:"error,omitempty"`
}

// System is the host-level summary (load, memory, temperature,
// inferred throttling state).
type System struct {
	Load1m        float64 `json:"load_1m"`
	Load5m        float64 `json:"load_5m"`
	Load15m       float64 `json:"load_15m"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	TempCelsius   float64 `json:"temp_celsius"`
	Throttled     bool    `json:"throttled"`
	Error         string  `json:"error,omitempty"`
}

// Meta holds process-level info useful for the dashboard footer/header.
type Meta struct {
	Hostname      string `json:"hostname"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Commit        string `json:"commit"`
	Dirty         bool   `json:"dirty"`
	BuildTime     string `json:"build_time"`
	Role          string `json:"role"`
}

// Stats is the top-level response payload for GET /api/stats.
type Stats struct {
	Timestamp  int64                `json:"timestamp"`
	Interfaces map[string]Interface `json:"interfaces"`
	Hotspot    Hotspot              `json:"hotspot"`
	WAN        WAN                  `json:"wan"`
	Admin      Admin                `json:"admin"`
	System     System               `json:"system"`
	Meta       Meta                 `json:"meta"`
}

// Server wires configuration and startup-derived state into the HTTP handlers.
type Server struct {
	cfg      config.Config
	started  time.Time
	netstats *netstats.Collector
	hotspot  *hotspot.Collector
	wan      *wan.Collector
	admin    *admin.Collector
	system   *system.Collector
}

// New returns a Server configured with cfg and the given collectors.
// The netstats and system collectors are expected to already be
// running in their own goroutines; hotspot, wan, and admin collectors
// refresh lazily on read.
func New(cfg config.Config, ns *netstats.Collector, hs *hotspot.Collector, wn *wan.Collector, ad *admin.Collector, sys *system.Collector) *Server {
	return &Server{
		cfg:      cfg,
		started:  time.Now(),
		netstats: ns,
		hotspot:  hs,
		wan:      wn,
		admin:    ad,
		system:   sys,
	}
}

// wifiAdminCheck is a shared helper that validates the request method,
// admin role, and auth socket availability for all wifi endpoints.
// Returns false (and writes an error response) if any check fails.
func (s *Server) wifiAdminCheck(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if authz.RoleFromContext(r.Context()) != "admin" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if s.cfg.AuthSocket == "" {
		http.Error(w, "auth socket not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// jsonOK writes {"ok":true} with 200.
func jsonOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true}` + "\n"))
}

// jsonErr writes {"ok":false,"error":"..."} with the given status.
func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"ok": "false", "error": msg})
}

// WifiNetworks handles GET /api/wifi/networks (list) and
// POST /api/wifi/networks (add). Requires admin role.
func (s *Server) WifiNetworks() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if authz.RoleFromContext(r.Context()) != "admin" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if s.cfg.AuthSocket == "" {
				http.Error(w, "auth socket not configured", http.StatusServiceUnavailable)
				return
			}
			networks, err := authz.CallWifiList(r.Context(), s.cfg.AuthSocket)
			if err != nil {
				log.Printf("wifi list: %v", err)
				jsonErr(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "networks": networks})

		case http.MethodPost:
			if !s.wifiAdminCheck(w, r, http.MethodPost) {
				return
			}
			var body struct {
				SSID string `json:"ssid"`
				PSK  string `json:"psk"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			log.Printf("wifi add: ssid=%q requested by role=%q", body.SSID, authz.RoleFromContext(r.Context()))
			if err := authz.CallWifiAdd(r.Context(), s.cfg.AuthSocket, body.SSID, body.PSK); err != nil {
				log.Printf("wifi add: failed ssid=%q: %v", body.SSID, err)
				jsonErr(w, http.StatusBadRequest, err.Error())
				return
			}
			log.Printf("wifi add: ok ssid=%q", body.SSID)
			jsonOK(w)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// WifiNetwork handles DELETE /api/wifi/networks/{ssid}. Requires admin.
func (s *Server) WifiNetwork() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.wifiAdminCheck(w, r, http.MethodDelete) {
			return
		}
		// Extract SSID from path: /api/wifi/networks/{ssid}
		ssid := strings.TrimPrefix(r.URL.Path, "/api/wifi/networks/")
		if ssid == "" {
			jsonErr(w, http.StatusBadRequest, "SSID required in path")
			return
		}
		log.Printf("wifi remove: ssid=%q requested by role=%q", ssid, authz.RoleFromContext(r.Context()))
		if err := authz.CallWifiRemove(r.Context(), s.cfg.AuthSocket, ssid); err != nil {
			log.Printf("wifi remove: failed ssid=%q: %v", ssid, err)
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("wifi remove: ok ssid=%q", ssid)
		jsonOK(w)
	}
}

// WifiReload handles POST /api/wifi/reload. Requires admin.
func (s *Server) WifiReload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.wifiAdminCheck(w, r, http.MethodPost) {
			return
		}
		log.Printf("wifi reload: requested by role=%q", authz.RoleFromContext(r.Context()))
		if err := authz.CallWifiReload(r.Context(), s.cfg.AuthSocket); err != nil {
			log.Printf("wifi reload: failed: %v", err)
			jsonErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		log.Printf("wifi reload: ok")
		jsonOK(w)
	}
}

// WanOp handles POST /api/wan/up and /api/wan/down. Requires admin role.
// Calls the pispot-authd helper with the given op ("wan_up" or "wan_down").
func (s *Server) WanOp(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		role := authz.RoleFromContext(r.Context())
		log.Printf("wan op: %s requested by role=%q", op, role)
		if role != "admin" {
			log.Printf("wan op: %s denied — role=%q is not admin", op, role)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if s.cfg.AuthSocket == "" {
			log.Printf("wan op: %s denied — AUTH_SOCKET not configured", op)
			http.Error(w, "auth socket not configured", http.StatusServiceUnavailable)
			return
		}
		ok, errMsg, err := authz.CallOp(r.Context(), s.cfg.AuthSocket, op)
		if err != nil {
			log.Printf("wan op: %s socket error: %v", op, err)
			http.Error(w, "helper unavailable", http.StatusServiceUnavailable)
			return
		}
		if ok {
			log.Printf("wan op: %s succeeded", op)
		} else {
			log.Printf("wan op: %s failed: %s", op, errMsg)
		}
		if ok {
			jsonOK(w)
		} else {
			jsonErr(w, http.StatusInternalServerError, errMsg)
		}
	}
}

// wanFromCollector builds the JSON-facing WAN struct from the wan
// collector's latest snapshot. Last-good data is preserved on error
// and the error text is surfaced via WAN.Error.
func (s *Server) wanFromCollector(r *http.Request) WAN {
	out := WAN{Interface: s.cfg.WANIf}
	if s.wan == nil {
		return out
	}
	snap := s.wan.Snapshot(r.Context())
	if snap == nil {
		return out
	}
	info := snap.Info
	out = WAN{
		Interface:        info.Interface,
		InterfacePresent: info.InterfacePresent,
		Connected:        info.Connected,
		SSID:             info.SSID,
		BSSID:            info.BSSID,
		SignalDBm:        info.SignalDBm,
		FreqMHz:          info.FreqMHz,
		TxBitrateMbps:    info.TxBitrateMbps,
		IP:               info.IP,
		Gateway:          info.Gateway,
	}
	if snap.Err != nil {
		out.Error = snap.Err.Error()
	}
	return out
}

// systemFromCollector builds the JSON-facing System struct from the
// system collector's latest snapshot.
func (s *Server) systemFromCollector() System {
	var out System
	if s.system == nil {
		return out
	}
	snap := s.system.Snapshot()
	if snap == nil {
		return out
	}
	out = System{
		Load1m:        snap.Info.Load1m,
		Load5m:        snap.Info.Load5m,
		Load15m:       snap.Info.Load15m,
		MemTotalBytes: snap.Info.MemTotalBytes,
		MemUsedBytes:  snap.Info.MemUsedBytes,
		TempCelsius:   snap.Info.TempCelsius,
		Throttled:     snap.Info.Throttled,
	}
	if snap.Err != nil {
		out.Error = snap.Err.Error()
	}
	return out
}

// adminFromCollector builds the JSON-facing Admin struct from the admin
// collector's latest snapshot.
func (s *Server) adminFromCollector(r *http.Request) Admin {
	out := Admin{Interface: s.cfg.AdminIf}
	if s.admin == nil {
		return out
	}
	snap := s.admin.Snapshot(r.Context())
	if snap == nil {
		return out
	}
	out = Admin{
		Interface: snap.Info.Interface,
		IP:        snap.Info.IP,
		Gateway:   snap.Info.Gateway,
		Link:      snap.Info.Link,
	}
	if snap.Err != nil {
		out.Error = snap.Err.Error()
	}
	return out
}

// hotspotFromCollector builds the JSON-facing Hotspot struct from the
// hotspot collector's latest snapshot. On collector error the error
// text is surfaced in Hotspot.Error while the last-good client list is
// still returned.
func (s *Server) hotspotFromCollector(r *http.Request) Hotspot {
	out := Hotspot{
		Interface: s.cfg.HotspotIf,
		Clients:   []HotspotClient{},
	}
	if s.hotspot == nil {
		return out
	}
	snap := s.hotspot.Snapshot(r.Context())
	if snap == nil {
		return out
	}
	for _, c := range snap.Clients {
		out.Clients = append(out.Clients, HotspotClient{
			MAC:              c.MAC,
			IP:               c.IP,
			Hostname:         c.Hostname,
			SignalDBm:        c.SignalDBm,
			ConnectedSeconds: c.ConnectedSeconds,
			RxBytes:          c.RxBytes,
			TxBytes:          c.TxBytes,
		})
	}
	out.ClientCount = len(out.Clients)
	if snap.Err != nil {
		out.Error = snap.Err.Error()
	}
	return out
}

// interfacesFromNetstats builds the JSON-facing Interface map from the
// collector's latest snapshot. The returned map always contains entries
// for every interface the collector was configured to track, even if
// the interface is currently absent from /proc/net/dev (values are then
// zero and Up is false).
func (s *Server) interfacesFromNetstats() map[string]Interface {
	out := make(map[string]Interface)
	if s.netstats == nil {
		return out
	}
	snap := s.netstats.Snapshot()
	if snap == nil {
		return out
	}
	for name, v := range snap.Interfaces {
		out[name] = Interface{
			RxMbps:       v.RxMbps,
			TxMbps:       v.TxMbps,
			RxTotalBytes: v.RxTotalBytes,
			TxTotalBytes: v.TxTotalBytes,
			Up:           v.Up,
		}
	}
	return out
}

// Stats returns the /api/stats handler. All four sections are live as
// of M4: interfaces throughput (M2), hotspot clients (M3), WAN link
// info, and admin interface (M4).
func (s *Server) Stats() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		stats := Stats{
			Timestamp:  time.Now().Unix(),
			Interfaces: s.interfacesFromNetstats(),
			Hotspot:    s.hotspotFromCollector(r),
			WAN:        s.wanFromCollector(r),
			Admin:      s.adminFromCollector(r),
			System:     s.systemFromCollector(),
			Meta: Meta{
				Hostname:      host,
				UptimeSeconds: int64(time.Since(s.started).Seconds()),
				Commit:        buildinfo.Commit,
				Dirty:         buildinfo.IsDirty(),
				BuildTime:     buildinfo.BuildTime,
				Role:          authz.RoleFromContext(r.Context()),
			},
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(stats)
	}
}

// Healthz returns a trivial liveness endpoint.
func (s *Server) Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	}
}
