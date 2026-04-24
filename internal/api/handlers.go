// Package api provides HTTP handlers for the pispot-ui JSON API.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
	"github.com/mcs-net/pispot-ui/internal/hotspot"
	"github.com/mcs-net/pispot-ui/internal/netstats"
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
	Interface     string  `json:"interface"`
	Connected     bool    `json:"connected"`
	SSID          string  `json:"ssid"`
	BSSID         string  `json:"bssid"`
	SignalDBm     int     `json:"signal_dbm"`
	FreqMHz       int     `json:"freq_mhz"`
	TxBitrateMbps float64 `json:"tx_bitrate_mbps"`
	IP            string  `json:"ip"`
	Gateway       string  `json:"gateway"`
	Error         string  `json:"error,omitempty"`
}

// Admin is the eth0 (administration) summary.
type Admin struct {
	Interface string `json:"interface"`
	IP        string `json:"ip"`
	Link      bool   `json:"link"`
	Error     string `json:"error,omitempty"`
}

// Meta holds process-level info useful for the dashboard footer/header.
type Meta struct {
	Hostname      string `json:"hostname"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Version       string `json:"version"`
	Stub          bool   `json:"stub"`
}

// Stats is the top-level response payload for GET /api/stats.
type Stats struct {
	Timestamp  int64                `json:"timestamp"`
	Interfaces map[string]Interface `json:"interfaces"`
	Hotspot    Hotspot              `json:"hotspot"`
	WAN        WAN                  `json:"wan"`
	Admin      Admin                `json:"admin"`
	Meta       Meta                 `json:"meta"`
}

// Server wires configuration and startup-derived state into the HTTP handlers.
type Server struct {
	cfg      config.Config
	started  time.Time
	netstats *netstats.Collector
	hotspot  *hotspot.Collector
}

// New returns a Server configured with cfg and the given collectors.
// The netstats collector is expected to be running; the hotspot
// collector is queried on demand and refreshes itself lazily.
func New(cfg config.Config, ns *netstats.Collector, hs *hotspot.Collector) *Server {
	return &Server{cfg: cfg, started: time.Now(), netstats: ns, hotspot: hs}
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

// Stats returns the /api/stats handler. Interfaces (M2) and hotspot
// clients (M3) are live; WAN and admin sections remain stub data until
// M4. Meta.Stub stays true while any section is stubbed so the dashboard
// can flag mixed-truth responses.
func (s *Server) Stats() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		stats := Stats{
			Timestamp:  time.Now().Unix(),
			Interfaces: s.interfacesFromNetstats(),
			Hotspot:    s.hotspotFromCollector(r),
			WAN: WAN{
				Interface:     s.cfg.WANIf,
				Connected:     true,
				SSID:          "CoffeeShop-5G",
				BSSID:         "11:22:33:44:55:66",
				SignalDBm:     -62,
				FreqMHz:       5180,
				TxBitrateMbps: 150.0,
				IP:            "192.168.1.42",
				Gateway:       "192.168.1.1",
			},
			Admin: Admin{
				Interface: s.cfg.AdminIf,
				IP:        "169.254.10.5",
				Link:      false,
			},
			Meta: Meta{
				Hostname:      host,
				UptimeSeconds: int64(time.Since(s.started).Seconds()),
				Version:       s.cfg.Version,
				Stub:          true,
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
