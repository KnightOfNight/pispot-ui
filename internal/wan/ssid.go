// Package wan collects upstream-link information for the WAN-facing
// wireless interface: SSID/BSSID/signal from `iw dev <iface> link`, IP
// from `ip -j addr show <iface>`, and default gateway from
// `ip -j route show default`.
//
// The collector refreshes lazily on request with a short TTL. Last-good
// data is retained across transient errors; legitimate disconnection
// (iw reports "Not connected.") is represented by Connected=false with
// every other field cleared.
package wan

import (
	"bufio"
	"encoding/json"
	"strconv"
	"strings"
)

// Info is the public, flattened view of the WAN interface's current
// upstream association. When Connected is false all other fields are
// zero/empty by design. InterfacePresent is false when the configured
// WAN interface does not exist in sysfs (e.g. USB Wi-Fi not plugged in).
type Info struct {
	Interface        string
	InterfacePresent bool
	Connected        bool
	SSID             string
	BSSID            string
	SignalDBm        int
	FreqMHz          int
	TxBitrateMbps    float64
	IP               string
	Gateway          string
}

// linkResult is the intermediate parsed view of `iw dev <iface> link`.
// Fields not populated by the parser remain zero-valued.
type linkResult struct {
	connected     bool
	ssid          string
	bssid         string
	signalDBm     int
	freqMHz       int
	txBitrateMbps float64
}

// parseIwLink parses `iw dev <iface> link` output.
//
// Connected form:
//
//	Connected to 11:22:33:44:55:66 (on wlan1)
//	        SSID: CoffeeShop-5G
//	        freq: 5180
//	        signal: -62 dBm
//	        tx bitrate: 150.0 MBit/s
//	        ...
//
// Disconnected form:
//
//	Not connected.
//
// The BSSID is normalized to lowercase.
func parseIwLink(raw []byte) linkResult {
	var out linkResult
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Not connected") {
			return linkResult{} // Connected=false, everything else zero.
		}
		if strings.HasPrefix(line, "Connected to ") {
			out.connected = true
			// "Connected to <bssid> (on <iface>)"
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				out.bssid = strings.ToLower(fields[2])
			}
			continue
		}
		key, value, ok := splitKeyValue(line)
		if !ok {
			continue
		}
		switch key {
		case "SSID":
			out.ssid = value
		case "freq":
			// Some drivers/iw versions emit freq as a decimal
			// ("5220.0") rather than a bare integer. Parse as float
			// and truncate to int MHz — real-world frequencies are
			// always whole MHz so no precision is lost.
			out.freqMHz = int(parseFirstFloat(value))
		case "signal":
			// "-62 dBm" or "-62 [-63 -64] dBm"
			out.signalDBm = parseFirstInt(value)
		case "tx bitrate":
			// "150.0 MBit/s"
			out.txBitrateMbps = parseFirstFloat(value)
		}
	}
	return out
}

// parseIPAddr extracts the first IPv4 address from `ip -j addr show <iface>`
// output. Returns "" if no IPv4 address is present (IPv6-only, admin-only,
// or empty output).
//
// Format (abridged):
//
//	[
//	  {
//	    "ifname": "wlan1",
//	    "addr_info": [
//	      {"family": "inet",  "local": "192.168.1.42", "prefixlen": 24, ...},
//	      {"family": "inet6", "local": "fe80::...",    "prefixlen": 64, ...}
//	    ]
//	  }
//	]
func parseIPAddr(raw []byte) string {
	var doc []struct {
		AddrInfo []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, iface := range doc {
		for _, a := range iface.AddrInfo {
			if a.Family == "inet" && a.Local != "" {
				return a.Local
			}
		}
	}
	return ""
}

// parseIPRoute extracts the gateway address from `ip -j route show default`
// whose `dev` matches iface. Returns "" if no matching default route is
// present. If multiple matching routes exist the first is returned.
//
// Format (abridged):
//
//	[
//	  {"dst": "default", "gateway": "192.168.1.1", "dev": "wlan1", ...},
//	  {"dst": "default", "gateway": "10.0.0.1",    "dev": "eth0",  ...}
//	]
func parseIPRoute(raw []byte, iface string) string {
	var doc []struct {
		Dst     string `json:"dst"`
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, r := range doc {
		if r.Dev == iface && r.Gateway != "" {
			return r.Gateway
		}
	}
	return ""
}

// splitKeyValue splits on the first colon; both halves are trimmed.
func splitKeyValue(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}

// parseFirstInt returns the leading signed int from s, 0 on failure.
func parseFirstInt(s string) int {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	return n
}

// parseFirstFloat returns the leading float from s, 0 on failure.
func parseFirstFloat(s string) float64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return n
}
