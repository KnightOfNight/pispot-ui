// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
)

// Config holds runtime configuration for pispot-ui.
type Config struct {
	ListenAddr string
	HotspotIf  string
	WANIf      string
	AdminIf    string
	ProcPath   string
	SysPath    string
	LeasesPath string
}

// Load reads configuration from environment variables, applying defaults
// suitable for running inside the pispot-ui Docker container on the Pi.
func Load() Config {
	return Config{
		ListenAddr: getenv("LISTEN_ADDR", ":8080"),
		HotspotIf:  getenv("HOTSPOT_IF", "wlan0"),
		WANIf:      getenv("WAN_IF", "wlan1"),
		AdminIf:    getenv("ADMIN_IF", "eth0"),
		ProcPath:   getenv("PROC_PATH", "/proc"),
		SysPath:    getenv("SYS_PATH", "/sys"),
		LeasesPath: getenv("LEASES_PATH", "/var/lib/misc/dnsmasq.leases"),
	}
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
