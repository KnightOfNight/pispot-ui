package netstats

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sampleProcNetDev is a realistic fixture matching the layout of the real
// file on Linux. Used across several test cases.
const sampleProcNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  100000    500    0    0    0     0          0         0   100000    500    0    0    0     0       0          0
 wlan0: 1000000   1000    0    0    0     0          0         0   500000    800    0    0    0     0       0          0
 wlan1: 2000000   2000    0    0    0     0          0         0   750000    900    0    0    0     0       0          0
  eth0:   50000     20    0    0    0     0          0         0    30000     15    0    0    0     0       0          0
`

// Test 1 — parser extracts rx/tx bytes per interface correctly, and
// ignores header and malformed lines.
func TestParseProcNetDev(t *testing.T) {
	got := parseProcNetDev([]byte(sampleProcNetDev))

	want := map[string]parsedLine{
		"lo":    {rxBytes: 100000, txBytes: 100000},
		"wlan0": {rxBytes: 1000000, txBytes: 500000},
		"wlan1": {rxBytes: 2000000, txBytes: 750000},
		"eth0":  {rxBytes: 50000, txBytes: 30000},
	}

	if len(got) != len(want) {
		t.Fatalf("interface count mismatch: got %d, want %d (%v)", len(got), len(want), got)
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("missing interface %q in parse result", name)
			continue
		}
		if g != w {
			t.Errorf("%s: got %+v, want %+v", name, g, w)
		}
	}

	// Malformed/empty content should not panic and should return empty.
	if len(parseProcNetDev([]byte(""))) != 0 {
		t.Errorf("empty input: expected empty result")
	}
	if len(parseProcNetDev([]byte("not a valid line\n"))) != 0 {
		t.Errorf("malformed input: expected empty result")
	}
}

// Test 2 — rate math across a full lifecycle: first sample yields 0
// rates but valid totals; second sample yields correct Mbps; counter
// reset yields 0; and a missing interface yields Up=false with zeros.
func TestCollectorRates(t *testing.T) {
	// Controlled clock that advances in fixed steps.
	var now atomic.Int64
	baseNs := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	now.Store(baseNs)
	clock := func() time.Time {
		return time.Unix(0, now.Load())
	}
	advance := func(d time.Duration) {
		now.Add(int64(d))
	}

	// Controllable source: we swap its return value between ticks.
	var sourceMu sync.Mutex
	sourcePayload := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
 wlan0: 1000000   1000    0    0    0     0          0         0   500000    800    0    0    0     0       0          0
`
	source := func() ([]byte, error) {
		sourceMu.Lock()
		defer sourceMu.Unlock()
		return []byte(sourcePayload), nil
	}
	setSource := func(s string) {
		sourceMu.Lock()
		sourcePayload = s
		sourceMu.Unlock()
	}

	// operstate: wlan0 up, wlan1 down (used in later missing-iface case).
	operstate := func(name string) (string, error) {
		switch name {
		case "wlan0":
			return "up", nil
		case "wlan1":
			return "down", nil
		default:
			return "", fmt.Errorf("no such interface %q", name)
		}
	}

	c := newWithDeps(
		[]string{"wlan0", "wlan1"},
		source,
		operstate,
		clock,
		sampleInterval,
	)

	// First tick: no prev sample -> rates must be 0, totals must reflect
	// the raw counters, Up should come from operstate.
	c.tick()
	snap := c.Snapshot()
	w0 := snap.Interfaces["wlan0"]
	if w0.RxMbps != 0 || w0.TxMbps != 0 {
		t.Errorf("first tick wlan0: expected 0 rates, got rx=%v tx=%v", w0.RxMbps, w0.TxMbps)
	}
	if w0.RxTotalBytes != 1_000_000 || w0.TxTotalBytes != 500_000 {
		t.Errorf("first tick wlan0 totals: got rx=%d tx=%d", w0.RxTotalBytes, w0.TxTotalBytes)
	}
	if !w0.Up {
		t.Errorf("first tick wlan0: expected Up=true")
	}

	// wlan1 is absent from /proc/net/dev AND operstate reports "down".
	w1 := snap.Interfaces["wlan1"]
	if w1.Up {
		t.Errorf("first tick wlan1: expected Up=false (operstate=down)")
	}
	if w1.RxTotalBytes != 0 || w1.TxTotalBytes != 0 {
		t.Errorf("first tick wlan1: expected zero totals, got rx=%d tx=%d", w1.RxTotalBytes, w1.TxTotalBytes)
	}

	// Advance clock 1 s; update source with +1_250_000 rx and +250_000 tx.
	// Expected: rx 10 Mbps, tx 2 Mbps (1_250_000*8/1e6/1s = 10).
	advance(1 * time.Second)
	setSource(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
 wlan0: 2250000   1500    0    0    0     0          0         0   750000   1100    0    0    0     0       0          0
`)
	c.tick()
	snap = c.Snapshot()
	w0 = snap.Interfaces["wlan0"]
	if w0.RxMbps != 10 {
		t.Errorf("second tick wlan0 rx: got %v Mbps, want 10", w0.RxMbps)
	}
	if w0.TxMbps != 2 {
		t.Errorf("second tick wlan0 tx: got %v Mbps, want 2", w0.TxMbps)
	}
	if w0.RxTotalBytes != 2_250_000 || w0.TxTotalBytes != 750_000 {
		t.Errorf("second tick wlan0 totals: got rx=%d tx=%d", w0.RxTotalBytes, w0.TxTotalBytes)
	}

	// Third tick: counters reset (e.g. interface reassociation).
	// Rates must report 0, totals reflect the new (smaller) values.
	advance(1 * time.Second)
	setSource(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
 wlan0:  100000    100    0    0    0     0          0         0    50000     80    0    0    0     0       0          0
`)
	c.tick()
	snap = c.Snapshot()
	w0 = snap.Interfaces["wlan0"]
	if w0.RxMbps != 0 || w0.TxMbps != 0 {
		t.Errorf("reset tick wlan0: expected 0 rates, got rx=%v tx=%v", w0.RxMbps, w0.TxMbps)
	}
	if w0.RxTotalBytes != 100_000 || w0.TxTotalBytes != 50_000 {
		t.Errorf("reset tick wlan0 totals: got rx=%d tx=%d", w0.RxTotalBytes, w0.TxTotalBytes)
	}
}

// Test 3 — degenerate elapsed interval (0 seconds between samples) must
// not panic on divide-by-zero and must return 0 rates.
func TestComputeMbpsDegenerate(t *testing.T) {
	now := time.Unix(0, 0)

	// Zero elapsed.
	if r := computeMbps(0, 1_000_000, now, now); r != 0 {
		t.Errorf("zero elapsed: got %v, want 0", r)
	}

	// Negative elapsed (clock went backward).
	back := now.Add(-1 * time.Second)
	if r := computeMbps(0, 1_000_000, now, back); r != 0 {
		t.Errorf("negative elapsed: got %v, want 0", r)
	}

	// Counter regression.
	if r := computeMbps(2_000_000, 1_000_000, now, now.Add(time.Second)); r != 0 {
		t.Errorf("counter regression: got %v, want 0", r)
	}

	// Known-good sanity.
	if r := computeMbps(0, 1_250_000, now, now.Add(time.Second)); r != 10 {
		t.Errorf("1.25 MB/s: got %v, want 10", r)
	}
}

// Test 4 — concurrency smoke: collector.Run must be cancelable via
// context, Snapshot() must be safe to call from another goroutine during
// collection, and no data races must occur under `go test -race`.
func TestCollectorConcurrentSnapshot(t *testing.T) {
	source := func() ([]byte, error) {
		return []byte(sampleProcNetDev), nil
	}
	operstate := func(string) (string, error) { return "up", nil }

	c := newWithDeps(
		[]string{"wlan0", "wlan1", "eth0"},
		source,
		operstate,
		time.Now,
		10*time.Millisecond, // fast interval to drive ticks during the test
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	// Hammer Snapshot() for a short window.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s := c.Snapshot()
				if s == nil {
					t.Errorf("nil snapshot observed")
					return
				}
				if _, ok := s.Interfaces["wlan0"]; !ok {
					t.Errorf("snapshot missing wlan0: %v", s.Interfaces)
					return
				}
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("collector did not stop within 500ms of cancel")
	}
}
