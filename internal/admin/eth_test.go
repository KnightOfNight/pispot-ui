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

// Test 1 — operstate maps to Link bool correctly for the four observed
// states, and a missing file results in Link=false with Err set.
func TestOperstateMapping(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(sampleIPAddr), nil
	}
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
// or empty input.
func TestParseIPAddrAdmin(t *testing.T) {
	if got := parseIPAddr([]byte(sampleIPAddr)); got != "10.0.0.5" {
		t.Errorf("single: got %q, want 10.0.0.5", got)
	}
	if got := parseIPAddr([]byte(sampleIPAddrEmpty)); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
	if got := parseIPAddr([]byte("garbage")); got != "" {
		t.Errorf("garbage: got %q, want empty", got)
	}
}

// Test 3 — Combined collector behavior: TTL caching collapses calls;
// ip-addr failure leaves link state intact, retains last-good IP, and
// surfaces the error via Snapshot.Err.
func TestCollectorTTLAndLastGood(t *testing.T) {
	var addrCalls atomic.Int64
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		addrCalls.Add(1)
		return []byte(sampleIPAddr), nil
	}
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
	for i := 0; i < 20; i++ {
		_ = c.Snapshot(context.Background())
	}
	if addrCalls.Load() != 1 {
		t.Errorf("within-TTL: expected 1 ip call, got %d", addrCalls.Load())
	}

	// Inject an ip-addr failure after TTL expiry; last-good IP must be
	// retained and the error surfaced.
	addrErr := errors.New("ip boom")
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, addrErr
	}
	now.Add(int64(ttl + time.Second))
	snap := c.Snapshot(context.Background())
	if snap.Err == nil || !errors.Is(snap.Err, addrErr) {
		t.Errorf("expected wrapped ip error, got %v", snap.Err)
	}
	if snap.Info.IP != "10.0.0.5" {
		t.Errorf("last-good IP lost: got %q, want 10.0.0.5", snap.Info.IP)
	}
	// Link state should still reflect current operstate even during
	// ip-addr failure, since operstate is read independently.
	if !snap.Info.Link {
		t.Errorf("Link should still be true on ip-addr-only failure")
	}
}
