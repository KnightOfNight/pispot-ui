// Package hotspot collects information about stations associated with
// the LAN-side access point (wlan0), enriched with IP/hostname data
// from the dnsmasq lease file.
//
// The collector shells out to `iw dev <iface> station dump` to enumerate
// associated stations and parses the result. Results are cached for a
// short TTL so callers (the HTTP API) can poll aggressively without
// thrashing iw.
package hotspot

import (
	"bufio"
	"strconv"
	"strings"
)

// Client is one associated station, optionally enriched with a DHCP-
// derived IP and hostname from the dnsmasq lease file.
type Client struct {
	MAC              string
	IP               string
	Hostname         string
	SignalDBm        int
	ConnectedSeconds uint64
	RxBytes          uint64
	TxBytes          uint64
}

// parseStationDump parses the output of `iw dev <iface> station dump` and
// returns the list of associated stations. MAC addresses are normalized
// to lowercase. Unknown key/value lines are ignored, so future iw
// versions adding new fields will not break parsing.
//
// Relevant format (indented key/value pairs under each Station header):
//
//	Station aa:bb:cc:dd:ee:01 (on wlan0)
//	        inactive time:  120 ms
//	        rx bytes:       5123456
//	        rx packets:     12345
//	        tx bytes:       1234567
//	        signal:         -55 dBm
//	        connected time: 1234 seconds
//	Station aa:bb:cc:dd:ee:02 (on wlan0)
//	        ...
func parseStationDump(raw []byte) []Client {
	var (
		clients []Client
		cur     *Client
	)

	flush := func() {
		if cur != nil {
			clients = append(clients, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	// iw rows can be long; grow the buffer just in case.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "Station ") {
			flush()
			// "Station <mac> (on <iface>)"
			fields := strings.Fields(trimmed)
			if len(fields) < 2 {
				continue
			}
			cur = &Client{MAC: strings.ToLower(fields[1])}
			continue
		}
		if cur == nil {
			// Key/value line outside of a Station block; skip.
			continue
		}

		key, value, ok := splitKeyValue(trimmed)
		if !ok {
			continue
		}

		switch key {
		case "rx bytes":
			cur.RxBytes = parseUint(value)
		case "tx bytes":
			cur.TxBytes = parseUint(value)
		case "signal":
			// "-55 dBm" or "-55 [-57] dBm"
			cur.SignalDBm = parseFirstInt(value)
		case "connected time":
			// "1234 seconds"
			cur.ConnectedSeconds = parseUint(value)
		}
	}
	flush()
	return clients
}

// splitKeyValue splits a line of the form "key: value" on the first
// colon. Both halves are trimmed. Returns ok=false if no colon is found.
func splitKeyValue(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}

// parseUint returns the leading unsigned integer from s, or 0 if the
// first token does not parse. iw always emits a bare decimal followed
// by a unit word, so taking the first whitespace-separated token is
// sufficient.
func parseUint(s string) uint64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseFirstInt returns the leading signed integer from s, or 0 on
// parse failure. Used for dBm signal values which are negative.
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
