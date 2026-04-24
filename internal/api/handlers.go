// Package api provides HTTP handlers for the pispot-ui JSON API.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
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
	Hostname       string `json:"hostname"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	Version        string `json:"version"`
	Stub           bool   `json:"stub"`
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
	cfg     config.Config
	started time.Time
}

// New returns a Server configured with cfg.
func New(cfg config.Config) *Server {
	return &Server{cfg: cfg, started: time.Now()}
}

// Stats returns the /api/stats handler. For M1 it returns a stub payload
// that exercises every field in the schema so the frontend can be validated
// against the real API contract before live data is wired in.
func (s *Server) Stats() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		stats := Stats{
			Timestamp: time.Now().Unix(),
			Interfaces: map[string]Interface{
				s.cfg.HotspotIf: {RxMbps: 12.3, TxMbps: 4.1, RxTotalBytes: 9_000_000_000, TxTotalBytes: 1_200_000_000, Up: true},
				s.cfg.WANIf:     {RxMbps: 48.7, TxMbps: 22.1, RxTotalBytes: 15_000_000_000, TxTotalBytes: 3_400_000_000, Up: true},
				s.cfg.AdminIf:   {RxMbps: 0.0, TxMbps: 0.0, RxTotalBytes: 120_000, TxTotalBytes: 80_000, Up: false},
			},
			Hotspot: Hotspot{
				Interface:   s.cfg.HotspotIf,
				ClientCount: 2,
				Clients: []HotspotClient{
					{MAC: "aa:bb:cc:dd:ee:01", IP: "10.42.0.23", Hostname: "phone-01", SignalDBm: -55, ConnectedSeconds: 1234, RxBytes: 5_000_000, TxBytes: 1_500_000},
					{MAC: "aa:bb:cc:dd:ee:02", IP: "10.42.0.24", Hostname: "laptop-02", SignalDBm: -72, ConnectedSeconds: 300, RxBytes: 250_000, TxBytes: 90_000},
				},
			},
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
