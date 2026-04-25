package hotspot

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Realistic iw station dump fixture with two stations. Uses tab-indented
// key/value lines, matching iw's actual output.
const sampleStationDump = `Station AA:BB:CC:DD:EE:01 (on wlan0)
	inactive time:	120 ms
	rx bytes:	5123456
	rx packets:	12345
	tx bytes:	1234567
	tx packets:	8901
	rx drop misc:	0
	signal:  	-55 dBm
	signal avg:	-54 dBm
	tx bitrate:	150.0 MBit/s
	rx bitrate:	120.0 MBit/s
	authorized:	yes
	connected time:	1234 seconds
Station aa:bb:cc:dd:ee:02 (on wlan0)
	inactive time:	30 ms
	rx bytes:	250000
	tx bytes:	90000
	signal:  	-72 dBm
	connected time:	300 seconds
`

// Realistic dnsmasq leases fixture: one known hostname, one "*" (unknown),
// one commented line, and one unrelated MAC.
const sampleLeases = `# comment line, should be ignored
1777052341 aa:bb:cc:dd:ee:01 10.42.0.23 phone-01 01:aa:bb:cc:dd:ee:01
1777052500 AA:BB:CC:DD:EE:02 10.42.0.24 * *
1777052999 99:88:77:66:55:44 10.42.0.99 orphan-device 01:99:88:77:66:55:44
malformed line here
`

// Test 1 — iw parser extracts each station's fields correctly, with
// MAC addresses normalized to lowercase.
func TestParseStationDump(t *testing.T) {
	clients := parseStationDump([]byte(sampleStationDump))
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %d: %+v", len(clients), clients)
	}

	c0 := clients[0]
	if c0.MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("client 0 MAC: got %q, want lowercased aa:bb:cc:dd:ee:01", c0.MAC)
	}
	if c0.RxBytes != 5_123_456 {
		t.Errorf("client 0 rx_bytes: got %d, want 5123456", c0.RxBytes)
	}
	if c0.TxBytes != 1_234_567 {
		t.Errorf("client 0 tx_bytes: got %d, want 1234567", c0.TxBytes)
	}
	if c0.SignalDBm != -55 {
		t.Errorf("client 0 signal: got %d, want -55", c0.SignalDBm)
	}
	if c0.ConnectedSeconds != 1234 {
		t.Errorf("client 0 connected: got %d, want 1234", c0.ConnectedSeconds)
	}

	c1 := clients[1]
	if c1.MAC != "aa:bb:cc:dd:ee:02" {
		t.Errorf("client 1 MAC: got %q", c1.MAC)
	}
	if c1.SignalDBm != -72 {
		t.Errorf("client 1 signal: got %d, want -72", c1.SignalDBm)
	}
	if c1.ConnectedSeconds != 300 {
		t.Errorf("client 1 connected: got %d, want 300", c1.ConnectedSeconds)
	}

	// Empty / malformed input should yield no clients, no panic.
	if got := parseStationDump([]byte("")); len(got) != 0 {
		t.Errorf("empty input: expected 0 clients, got %d", len(got))
	}
	if got := parseStationDump([]byte("garbage\nno stations here\n")); len(got) != 0 {
		t.Errorf("garbage input: expected 0 clients, got %d", len(got))
	}
}

// Test 2 — lease parser extracts MAC→IP/hostname, handles "*" as empty,
// ignores comments and malformed lines, lowercases MAC keys.
func TestParseLeases(t *testing.T) {
	m := parseLeases([]byte(sampleLeases))

	// Known hostname.
	e1, ok := m["aa:bb:cc:dd:ee:01"]
	if !ok {
		t.Fatalf("missing aa:bb:cc:dd:ee:01 in leases")
	}
	if e1.IP != "10.42.0.23" || e1.Hostname != "phone-01" {
		t.Errorf("ee:01: got %+v, want IP=10.42.0.23 Hostname=phone-01", e1)
	}

	// "*" hostname should map to empty string; MAC case-insensitive key.
	e2, ok := m["aa:bb:cc:dd:ee:02"]
	if !ok {
		t.Fatalf("missing aa:bb:cc:dd:ee:02 (uppercased in source) in leases")
	}
	if e2.IP != "10.42.0.24" {
		t.Errorf("ee:02 IP: got %q, want 10.42.0.24", e2.IP)
	}
	if e2.Hostname != "" {
		t.Errorf("ee:02 Hostname: got %q, want empty (from *)", e2.Hostname)
	}

	// Comments and malformed lines ignored — total entry count is 3
	// (the three valid lines), not 5 (total raw lines including comment
	// and malformed).
	if len(m) != 3 {
		t.Errorf("expected 3 lease entries, got %d: %+v", len(m), m)
	}
}

// Test 3 — merge: parsed stations are enriched with lease data; stations
// without a matching lease retain empty IP/hostname; output is sorted
// by MAC ascending.
func TestCollectorMerge(t *testing.T) {
	// Fixture with 3 stations; only 2 have leases (third is unknown).
	stationDump := `Station bb:bb:bb:bb:bb:bb (on wlan0)
	signal:	-60 dBm
	connected time:	10 seconds
Station AA:AA:AA:AA:AA:AA (on wlan0)
	signal:	-50 dBm
	connected time:	20 seconds
Station cc:cc:cc:cc:cc:cc (on wlan0)
	signal:	-70 dBm
	connected time:	30 seconds
`
	leases := `1 aa:aa:aa:aa:aa:aa 10.0.0.1 alpha *
1 bb:bb:bb:bb:bb:bb 10.0.0.2 bravo *
`

	runIw := func(ctx context.Context, iface string) ([]byte, error) {
		return []byte(stationDump), nil
	}
	readLeases := func() ([]byte, error) {
		return []byte(leases), nil
	}

	exists := func(name string) bool { return true }
	c := newWithDeps("wlan0", runIw, readLeases, exists, time.Now, 100*time.Millisecond)
	snap := c.Snapshot(context.Background())

	if snap.Err != nil {
		t.Fatalf("unexpected error: %v", snap.Err)
	}
	if len(snap.Clients) != 3 {
		t.Fatalf("expected 3 clients, got %d", len(snap.Clients))
	}

	// Stable MAC-ascending order.
	wantOrder := []string{
		"aa:aa:aa:aa:aa:aa",
		"bb:bb:bb:bb:bb:bb",
		"cc:cc:cc:cc:cc:cc",
	}
	for i, want := range wantOrder {
		if snap.Clients[i].MAC != want {
			t.Errorf("sort position %d: got %q, want %q", i, snap.Clients[i].MAC, want)
		}
	}

	// Enriched entries have IP and hostname.
	if snap.Clients[0].IP != "10.0.0.1" || snap.Clients[0].Hostname != "alpha" {
		t.Errorf("aa: got IP=%q Host=%q, want 10.0.0.1/alpha",
			snap.Clients[0].IP, snap.Clients[0].Hostname)
	}
	if snap.Clients[1].IP != "10.0.0.2" || snap.Clients[1].Hostname != "bravo" {
		t.Errorf("bb: got IP=%q Host=%q, want 10.0.0.2/bravo",
			snap.Clients[1].IP, snap.Clients[1].Hostname)
	}

	// Unenriched entry has empty IP/Hostname.
	if snap.Clients[2].IP != "" || snap.Clients[2].Hostname != "" {
		t.Errorf("cc: got IP=%q Host=%q, want both empty",
			snap.Clients[2].IP, snap.Clients[2].Hostname)
	}

	// Missing lease file should not be fatal.
	readMissing := func() ([]byte, error) { return nil, fs.ErrNotExist }
	c2 := newWithDeps("wlan0", runIw, readMissing, exists, time.Now, 100*time.Millisecond)
	snap2 := c2.Snapshot(context.Background())
	if snap2.Err != nil {
		t.Errorf("missing leases: should not set Err, got: %v", snap2.Err)
	}
	if len(snap2.Clients) != 3 {
		t.Errorf("missing leases: expected 3 clients, got %d", len(snap2.Clients))
	}
	for _, cl := range snap2.Clients {
		if cl.IP != "" || cl.Hostname != "" {
			t.Errorf("missing leases: expected empty IP/Hostname, got %+v", cl)
		}
	}
}

// Test 4 — TTL honored, concurrent Snapshot calls collapse to a single
// iw invocation, and last-good snapshot is retained on iw error.
func TestCollectorTTLAndLastGood(t *testing.T) {
	var iwCalls atomic.Int64
	runIw := func(ctx context.Context, iface string) ([]byte, error) {
		iwCalls.Add(1)
		return []byte(sampleStationDump), nil
	}
	readLeases := func() ([]byte, error) { return []byte(sampleLeases), nil }

	// Controlled clock.
	var now atomic.Int64
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	now.Store(base)
	clock := func() time.Time { return time.Unix(0, now.Load()) }

	ttl := 5 * time.Second
	exists := func(name string) bool { return true }
	c := newWithDeps("wlan0", runIw, readLeases, exists, clock, ttl)

	// Hammer Snapshot from many goroutines within the TTL window. Only
	// one iw call should result.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				snap := c.Snapshot(context.Background())
				if snap == nil {
					t.Errorf("nil snapshot")
					return
				}
				if len(snap.Clients) != 2 {
					t.Errorf("expected 2 clients, got %d", len(snap.Clients))
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := iwCalls.Load(); got != 1 {
		t.Errorf("within-TTL: expected 1 iw call, got %d", got)
	}

	// Advance past TTL and call again — iw should run once more.
	now.Add(int64(ttl + time.Second))
	_ = c.Snapshot(context.Background())
	if got := iwCalls.Load(); got != 2 {
		t.Errorf("after-TTL: expected 2 iw calls, got %d", got)
	}

	// Swap in a failing iw func, advance time, verify last-good Clients
	// are retained and Err is surfaced.
	iwErr := errors.New("iw boom")
	c.runIw = func(ctx context.Context, iface string) ([]byte, error) {
		return nil, iwErr
	}
	now.Add(int64(ttl + time.Second))
	snap := c.Snapshot(context.Background())
	if snap.Err == nil {
		t.Errorf("expected Err to be set after iw failure")
	}
	if !errors.Is(snap.Err, iwErr) {
		t.Errorf("expected wrapped iw error, got: %v", snap.Err)
	}
	if len(snap.Clients) != 2 {
		t.Errorf("last-good: expected 2 clients retained, got %d", len(snap.Clients))
	}
}

// TestCollectorInterfaceAbsent — when existsFunc reports the interface
// is missing, refresh must short-circuit before iw is invoked and must
// surface ErrInterfaceAbsent.
func TestCollectorInterfaceAbsent(t *testing.T) {
	runIw := func(ctx context.Context, iface string) ([]byte, error) {
		t.Errorf("iw should not be called when interface is absent")
		return nil, errors.New("unexpected iw call")
	}
	readLeases := func() ([]byte, error) {
		t.Errorf("leases should not be read when interface is absent")
		return nil, nil
	}
	exists := func(name string) bool { return false }

	c := newWithDeps("wlan0", runIw, readLeases, exists, time.Now, 1*time.Millisecond)
	snap := c.Snapshot(context.Background())
	if !errors.Is(snap.Err, ErrInterfaceAbsent) {
		t.Errorf("expected ErrInterfaceAbsent, got %v", snap.Err)
	}
	if len(snap.Clients) != 0 {
		t.Errorf("absent: expected 0 clients, got %d", len(snap.Clients))
	}
}
