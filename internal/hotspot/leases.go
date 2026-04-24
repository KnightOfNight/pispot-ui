package hotspot

import (
	"bufio"
	"strings"
)

// leaseEntry holds the IP and hostname associated with a given MAC in
// the dnsmasq lease file.
type leaseEntry struct {
	IP       string
	Hostname string
}

// parseLeases reads the contents of /var/lib/misc/dnsmasq.leases and
// returns a map from lowercased MAC address to leaseEntry.
//
// dnsmasq lease file format (whitespace-separated, one lease per line):
//
//	<expiry-epoch> <mac> <ip> <hostname> <client-id>
//
// A hostname of "*" indicates unknown and is mapped to an empty string.
// Malformed lines are skipped silently. If a MAC appears more than once
// (e.g. stale entries), the last-seen entry wins — dnsmasq writes newest
// leases at the end.
func parseLeases(raw []byte) map[string]leaseEntry {
	out := make(map[string]leaseEntry)
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		// Need at least: expiry, mac, ip, hostname. client-id is optional.
		if len(fields) < 4 {
			continue
		}
		mac := strings.ToLower(fields[1])
		ip := fields[2]
		hostname := fields[3]
		if hostname == "*" {
			hostname = ""
		}
		out[mac] = leaseEntry{IP: ip, Hostname: hostname}
	}
	return out
}
