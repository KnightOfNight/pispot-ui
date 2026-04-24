package admin

import (
	"context"
	"errors"
	"io/fs"
	"sync/atomic"
	"testing"
	"time"
)

const sampleIPAddr = `[
  {
    "ifname": "eth0",
    "addr_info": [
      {"family": "inet",  "local": "10.0.0.5", "prefixlen": 24},
      {"family": "inet6", "local": "fe80::1",   "prefixlen": 64}
    ]
  }
]`

const sampleIPAddrEmpty = `[]`

const sampleIPRoute = `[
  {"dst": "default", "gateway": "10.0.0.1",   "dev": "eth0"},
  {"dst": "default", "gateway": "192.168.1.1", "dev": "wlan1"}
]`

const sampleIPRouteNoMatch = `[
  {"dst": "default", "gateway": "192.168.1.1", "dev": "wlan1"}
]`

// dispatchRun is a small helper that dispatches to the right fixture
// (or error) based on the command being invoked. Tests compose it with
// per-subcommand callbacks they control.
func dispatchRun(onAddr, onRoute func() ([]byte, error)) execFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ip" && len(args) >= 2 {
			switch args[1] {
			case "addr":
				return onAddr()
			case "route":
				return onRoute()
			}
		}
		return nil, errors.New("unexpected command")
	}
}

// Test 1 — operstate maps to Link bool correctly for the four observed
// states, and a missing file results in Link=false with Err set.
func TestOperstateMapping(t *testing.T) {
	run := dispatchRun(
		func() ([]byte, error) { return []byte(sampleIPAddr), nil },
		func() ([]byte, error) { return []byte(sampleIPRoute), nil },
	)
	cases := []struct {
		state    string
		readErr  error
		wantLink bool
		wantErr  bool
	}{
		{"up", nil, true, false},
		{"down", nil, false, false},
		{"unknown", nil, false, false},
		{"dormant", nil, false, false},
		{"", fs.ErrNotExist, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			op := func(name string) (string, error) {
				return tc.state, tc.readErr
			}
			c := newWithDeps("eth0", op, run, time.Now, 1*time.Millisecond)
			snap := c.Snapshot(context.Background())
			if snap.Info.Link != tc.wantLink {
				t.Errorf("state=%q: Link got %v, want %v", tc.state, snap.Info.Link, tc.wantLink)
			}
			if (snap.Err != nil) != tc.wantErr {
				t.Errorf("state=%q: Err=%v, wantErr=%v", tc.state, snap.Err, tc.wantErr)
			}
		})
	}
}

// Test 2 — parseIPAddr picks the first IPv4, returns empty for IPv6-only
// or empty input. Also covers parseIPRoute: dev match, no match, garbage.
func TestParseIPHelpers(t *testing.T) {
	// parseIPAddr.
	if got := parseIPAddr([]byte(sampleIPAddr)); got != "10.0.0.5" {
		t.Errorf("addr single: got %q, want 10.0.0.5", got)
	}
	if got := parseIPAddr([]byte(sampleIPAddrEmpty)); got != "" {
		t.Errorf("addr empty: got %q, want empty", got)
	}
	if got := parseIPAddr([]byte("garbage")); got != "" {
		t.Errorf("addr garbage: got %q, want empty", got)
	}

	// parseIPRoute.
	if got := parseIPRoute([]byte(sampleIPRoute), "eth0"); got != "10.0.0.1" {
		t.Errorf("route match: got %q, want 10.0.0.1", got)
	}
	if got := parseIPRoute([]byte(sampleIPRouteNoMatch), "eth0"); got != "" {
		t.Errorf("route no match: got %q, want empty", got)
	}
	if got := parseIPRoute([]byte("garbage"), "eth0"); got != "" {
		t.Errorf("route garbage: got %q, want empty", got)
	}
}

// Test 3 — Combined collector behavior: TTL caching collapses calls;
// gateway is fetched on successful addr; ip-addr failure retains last
// good IP+Gateway and surfaces the error; route-fetch failure alone is
// non-fatal (gateway goes blank, no Err set).
func TestCollectorTTLAndLastGood(t *testing.T) {
	var addrCalls, routeCalls atomic.Int64
	run := dispatchRun(
		func() ([]byte, error) { addrCalls.Add(1); return []byte(sampleIPAddr), nil },
		func() ([]byte, error) { routeCalls.Add(1); return []byte(sampleIPRoute), nil },
	)
	op := func(name string) (string, error) { return "up", nil }

	var now atomic.Int64
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	now.Store(base)
	clock := func() time.Time { return time.Unix(0, now.Load()) }

	ttl := 5 * time.Second
	c := newWithDeps("eth0", op, run, clock, ttl)

	first := c.Snapshot(context.Background())
	if !first.Info.Link || first.Info.IP != "10.0.0.5" {
		t.Fatalf("first: got %+v, want Link=true IP=10.0.0.5", first.Info)
	}
	if first.Info.Gateway != "10.0.0.1" {
		t.Fatalf("first: got Gateway=%q, want 10.0.0.1", first.Info.Gateway)
	}
	for i := 0; i < 20; i++ {
		_ = c.Snapshot(context.Background())
	}
	if addrCalls.Load() != 1 || routeCalls.Load() != 1 {
		t.Errorf("within-TTL: expected 1 addr + 1 route call, got addr=%d route=%d",
			addrCalls.Load(), routeCalls.Load())
	}

	// Route-only failure: gateway should blank out, other fields stay,
	// no error surfaced.
	c.run = dispatchRun(
		func() ([]byte, error) { return []byte(sampleIPAddr), nil },
		func() ([]byte, error) { return nil, errors.New("route boom") },
	)
	now.Add(int64(ttl + time.Second))
	snap := c.Snapshot(context.Background())
	if snap.Err != nil {
		t.Errorf("route-only failure should be non-fatal; got Err=%v", snap.Err)
	}
	if snap.Info.Gateway != "" {
		t.Errorf("route-only failure: expected blank Gateway, got %q", snap.Info.Gateway)
	}
	if snap.Info.IP != "10.0.0.5" {
		t.Errorf("route-only failure: IP should remain, got %q", snap.Info.IP)
	}

	// addr failure: last-good IP and Gateway must be retained (note
	// the previous tick wrote Gateway="" due to route failure above,
	// so we expect "" here — verifying retention semantics regardless).
	// First repopulate a known-good state.
	c.run = dispatchRun(
		func() ([]byte, error) { return []byte(sampleIPAddr), nil },
		func() ([]byte, error) { return []byte(sampleIPRoute), nil },
	)
	now.Add(int64(ttl + time.Second))
	_ = c.Snapshot(context.Background())
	// Now inject addr failure.
	addrErr := errors.New("ip boom")
	c.run = dispatchRun(
		func() ([]byte, error) { return nil, addrErr },
		func() ([]byte, error) { return []byte(sampleIPRoute), nil },
	)
	now.Add(int64(ttl + time.Second))
	snap = c.Snapshot(context.Background())
	if snap.Err == nil || !errors.Is(snap.Err, addrErr) {
		t.Errorf("expected wrapped ip-addr error, got %v", snap.Err)
	}
	if snap.Info.IP != "10.0.0.5" {
		t.Errorf("last-good IP lost: got %q, want 10.0.0.5", snap.Info.IP)
	}
	if snap.Info.Gateway != "10.0.0.1" {
		t.Errorf("last-good Gateway lost: got %q, want 10.0.0.1", snap.Info.Gateway)
	}
	if !snap.Info.Link {
		t.Errorf("Link should still be true on ip-addr-only failure")
	}
}
