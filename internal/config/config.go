// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strings"
)

// Config holds runtime configuration for pispot-ui.
type Config struct {
	ListenAddr  string
	HotspotIf   string
	WANIf       string
	AdminIf     string
	ProcPath    string
	SysPath     string
	LeasesPath  string
	TLSCertFile string
	TLSKeyFile  string
	RequireTLS  bool
	AuthSocket  string
	AuthRealm   string
}

// Load reads configuration from environment variables, applying defaults
// suitable for running inside the pispot-ui Docker container on the Pi.
func Load() Config {
	return Config{
		ListenAddr:  getenv("LISTEN_ADDR", ":8080"),
		HotspotIf:   getenv("HOTSPOT_IF", "wlan0"),
		WANIf:       getenv("WAN_IF", "wlan1"),
		AdminIf:     getenv("ADMIN_IF", "eth0"),
		ProcPath:    getenv("PROC_PATH", "/proc"),
		SysPath:     getenv("SYS_PATH", "/sys"),
		LeasesPath:  getenv("LEASES_PATH", "/var/lib/misc/dnsmasq.leases"),
		TLSCertFile: getenv("TLS_CERT_FILE", ""),
		TLSKeyFile:  getenv("TLS_KEY_FILE", ""),
		RequireTLS:  getenvBool("REQUIRE_TLS", false),
		AuthSocket:  getenv("AUTH_SOCKET", ""),
		AuthRealm:   getenv("AUTH_REALM", "N1QZS Radio Hotspot"),
	}
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
