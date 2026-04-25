package wan

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

const sampleIwLinkConnected = `Connected to 11:22:33:44:55:66 (on wlan1)
	SSID: CoffeeShop-5G
	freq: 5180
	RX: 1234567 bytes (1000 packets)
	TX: 234567 bytes (500 packets)
	signal: -62 dBm
	tx bitrate: 150.0 MBit/s
	bss flags: short-preamble short-slot-time
	dtim period: 2
	beacon int: 100
`

const sampleIwLinkDisconnected = `Not connected.
`

const sampleIPAddr = `[
  {
    "ifname": "wlan1",
    "addr_info": [
      {"family": "inet",  "local": "192.168.1.42", "prefixlen": 24},
      {"family": "inet6", "local": "fe80::1",       "prefixlen": 64}
    ]
  }
]`

const sampleIPAddrEmpty = `[]`

const sampleIPAddrIPv6Only = `[
  {
    "ifname": "wlan1",
    "addr_info": [
      {"family": "inet6", "local": "fe80::1", "prefixlen": 64}
    ]
  }
]`

const sampleIPRoute = `[
  {"dst": "default", "gateway": "192.168.1.1", "dev": "wlan1"},
  {"dst": "default", "gateway": "10.0.0.1",    "dev": "eth0"}
]`

const sampleIPRouteNoMatch = `[
  {"dst": "default", "gateway": "10.0.0.1", "dev": "eth0"}
]`

const sampleIPRouteMultiple = `[
  {"dst": "default", "gateway": "192.168.1.1", "dev": "wlan1"},
  {"dst": "default", "gateway": "192.168.2.1", "dev": "wlan1"}
]`

// Test 1 — iw link parser: happy path + disconnected form.
func TestParseIwLink(t *testing.T) {
	got := parseIwLink([]byte(sampleIwLinkConnected))
	if !got.connected {
		t.Errorf("connected: expected true, got false")
	}
	if got.bssid != "11:22:33:44:55:66" {
		t.Errorf("bssid: got %q, want 11:22:33:44:55:66 (lowercase)", got.bssid)
	}
	if got.ssid != "CoffeeShop-5G" {
		t.Errorf("ssid: got %q, want CoffeeShop-5G", got.ssid)
	}
	if got.signalDBm != -62 {
		t.Errorf("signal: got %d, want -62", got.signalDBm)
	}
	if got.freqMHz != 5180 {
		t.Errorf("freq: got %d, want 5180", got.freqMHz)
	}
	if got.txBitrateMbps != 150.0 {
		t.Errorf("tx_bitrate: got %v, want 150.0", got.txBitrateMbps)
	}

	// Bracketed per-chain signal — parser should take the first int.
	bracketed := "Connected to aa:bb:cc:dd:ee:ff (on wlan1)\n\tSSID: X\n\tsignal: -55 [-57 -58] dBm\n"
	if r := parseIwLink([]byte(bracketed)); r.signalDBm != -55 {
		t.Errorf("bracketed signal: got %d, want -55", r.signalDBm)
	}

	// Decimal freq form ("5220.0") as emitted by some brcmfmac
	// driver + iw combinations. Must parse to the integer MHz value.
	decimalFreq := "Connected to aa:bb:cc:dd:ee:ff (on wlan1)\n\tSSID: X\n\tfreq: 5220.0\n"
	if r := parseIwLink([]byte(decimalFreq)); r.freqMHz != 5220 {
		t.Errorf("decimal freq: got %d, want 5220", r.freqMHz)
	}

	// Decimal freq with explicit unit, for future-proofing against
	// yet another emission style.
	decimalUnitFreq := "Connected to aa:bb:cc:dd:ee:ff (on wlan1)\n\tSSID: X\n\tfreq: 5220.0 MHz\n"
	if r := parseIwLink([]byte(decimalUnitFreq)); r.freqMHz != 5220 {
		t.Errorf("decimal+unit freq: got %d, want 5220", r.freqMHz)
	}

	// Disconnected form clears everything.
	if r := parseIwLink([]byte(sampleIwLinkDisconnected)); r.connected || r.ssid != "" || r.bssid != "" {
		t.Errorf("disconnected: expected all zero, got %+v", r)
	}

	// Empty input is treated as disconnected (no fields set).
	if r := parseIwLink([]byte("")); r.connected {
		t.Errorf("empty: expected connected=false, got %+v", r)
	}

	// Uppercase BSSID in input must still be lowercased in output.
	up := "Connected to AA:BB:CC:DD:EE:FF (on wlan1)\n\tSSID: X\n"
	if r := parseIwLink([]byte(up)); r.bssid != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("uppercase bssid: got %q, want lowercased", r.bssid)
	}
}

// Test 2 — ip -j addr parser: IPv4 first, empty, IPv6-only.
func TestParseIPAddr(t *testing.T) {
	if got := parseIPAddr([]byte(sampleIPAddr)); got != "192.168.1.42" {
		t.Errorf("single IPv4: got %q, want 192.168.1.42", got)
	}
	if got := parseIPAddr([]byte(sampleIPAddrEmpty)); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
	if got := parseIPAddr([]byte(sampleIPAddrIPv6Only)); got != "" {
		t.Errorf("ipv6-only: got %q, want empty (ipv6 not surfaced)", got)
	}
	if got := parseIPAddr([]byte("not json at all")); got != "" {
		t.Errorf("garbage: got %q, want empty", got)
	}
}

// Test 3 — ip -j route parser: pick matching dev, skip others.
func TestParseIPRoute(t *testing.T) {
	if got := parseIPRoute([]byte(sampleIPRoute), "wlan1"); got != "192.168.1.1" {
		t.Errorf("matching: got %q, want 192.168.1.1", got)
	}
	if got := parseIPRoute([]byte(sampleIPRouteNoMatch), "wlan1"); got != "" {
		t.Errorf("no match: got %q, want empty", got)
	}
	if got := parseIPRoute([]byte(sampleIPRouteMultiple), "wlan1"); got != "192.168.1.1" {
		t.Errorf("multiple: first should win, got %q, want 192.168.1.1", got)
	}
	if got := parseIPRoute([]byte("not json"), "wlan1"); got != "" {
		t.Errorf("garbage: got %q, want empty", got)
	}
}

// Test 4 — Collector TTL honoring, concurrent call collapse, and
// last-good retention on error.
func TestCollectorTTLAndLastGood(t *testing.T) {
	var iwCalls, addrCalls, routeCalls atomic.Int64

	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "iw" && len(args) >= 3 && args[0] == "dev" && args[2] == "link":
			iwCalls.Add(1)
			return []byte(sampleIwLinkConnected), nil
		case name == "ip" && len(args) >= 2 && args[1] == "addr":
			addrCalls.Add(1)
			return []byte(sampleIPAddr), nil
		case name == "ip" && len(args) >= 2 && args[1] == "route":
			routeCalls.Add(1)
			return []byte(sampleIPRoute), nil
		}
		return nil, errUnexpectedCall
	}

	var now atomic.Int64
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	now.Store(base)
	clock := func() time.Time { return time.Unix(0, now.Load()) }

	ttl := 5 * time.Second
	exists := func(name string) bool { return true }
	c := newWithDeps("wlan1", run, exists, clock, ttl)

	// First Snapshot triggers a refresh; subsequent within-TTL calls don't.
	snap := c.Snapshot(context.Background())
	if !snap.Info.Connected {
		t.Fatalf("expected Connected=true; got %+v", snap.Info)
	}
	if snap.Info.SSID != "CoffeeShop-5G" || snap.Info.IP != "192.168.1.42" || snap.Info.Gateway != "192.168.1.1" {
		t.Errorf("merged info unexpected: %+v", snap.Info)
	}
	for i := 0; i < 50; i++ {
		_ = c.Snapshot(context.Background())
	}
	if iwCalls.Load() != 1 || addrCalls.Load() != 1 || routeCalls.Load() != 1 {
		t.Errorf("within-TTL: expected 1 call each, got iw=%d addr=%d route=%d",
			iwCalls.Load(), addrCalls.Load(), routeCalls.Load())
	}

	// Advance past TTL; next call refreshes.
	now.Add(int64(ttl + time.Second))
	_ = c.Snapshot(context.Background())
	if iwCalls.Load() != 2 {
		t.Errorf("post-TTL iw: got %d, want 2", iwCalls.Load())
	}

	// Now inject an iw error; last-good info must be retained.
	iwErr := errors.New("iw boom")
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "iw" {
			return nil, iwErr
		}
		return nil, errUnexpectedCall
	}
	now.Add(int64(ttl + time.Second))
	snap = c.Snapshot(context.Background())
	if snap.Err == nil || !errors.Is(snap.Err, iwErr) {
		t.Errorf("expected wrapped iw error, got %v", snap.Err)
	}
	if snap.Info.SSID != "CoffeeShop-5G" {
		t.Errorf("last-good info lost: got %+v", snap.Info)
	}

	// Disconnected form clears downstream fields (no stale IP/gateway).
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "iw" {
			return []byte(sampleIwLinkDisconnected), nil
		}
		// ip must not be called when iw says not connected.
		t.Errorf("ip should not be invoked when disconnected; called name=%q args=%v", name, args)
		return nil, errUnexpectedCall
	}
	now.Add(int64(ttl + time.Second))
	snap = c.Snapshot(context.Background())
	if snap.Info.Connected {
		t.Errorf("disconnected: expected Connected=false; got %+v", snap.Info)
	}
	if snap.Info.SSID != "" || snap.Info.IP != "" || snap.Info.Gateway != "" || snap.Info.BSSID != "" {
		t.Errorf("disconnected must clear downstream fields, got %+v", snap.Info)
	}
}

// TestCollectorInterfaceAbsent — when the existsFunc reports the
// configured interface is not present (e.g. USB Wi-Fi unplugged), the
// collector must skip all execs and surface ErrInterfaceAbsent.
func TestCollectorInterfaceAbsent(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("run should not be called when interface is absent; got %q %v", name, args)
		return nil, errUnexpectedCall
	}
	exists := func(name string) bool { return false }
	c := newWithDeps("wlan99", run, exists, time.Now, 1*time.Millisecond)

	snap := c.Snapshot(context.Background())
	if !errors.Is(snap.Err, ErrInterfaceAbsent) {
		t.Errorf("expected ErrInterfaceAbsent, got %v", snap.Err)
	}
	if snap.Info.Connected {
		t.Errorf("absent: expected Connected=false; got %+v", snap.Info)
	}
	if snap.Info.Interface != "wlan99" {
		t.Errorf("absent: Interface should still be set; got %q", snap.Info.Interface)
	}
}
