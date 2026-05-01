// Package netstats samples per-interface byte counters from /proc/net/dev
// at a fixed cadence and exposes a lock-free snapshot of the most recent
// Mbps rates and cumulative totals.
//
// The collector is intentionally minimal: one goroutine on a 1 s ticker,
// parsing /proc/net/dev and /sys/class/net/<iface>/operstate for only the
// interfaces listed at construction time. Readers obtain a point-in-time
// snapshot via Snapshot(), which is safe for concurrent use.
package netstats

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
)

// sampleInterval is the fixed cadence at which /proc/net/dev is read.
// Hard-coded for M2; may become configurable later.
const sampleInterval = 1 * time.Second

// InterfaceStats is the public, per-interface view of a single snapshot.
type InterfaceStats struct {
	Name         string
	Up           bool
	RxMbps       float64
	TxMbps       float64
	RxTotalBytes uint64
	TxTotalBytes uint64
}

// Snapshot is an immutable, point-in-time view of all tracked interfaces.
// It is published atomically; callers must treat it as read-only.
type Snapshot struct {
	At         time.Time
	Interfaces map[string]InterfaceStats
}

// sample is the raw counter state captured on one tick. It is kept inside
// the collector and used to compute deltas against the next tick.
type sample struct {
	rxBytes uint64
	txBytes uint64
	at      time.Time
	present bool // false when the interface was absent from /proc/net/dev
}

// Source supplies the raw bytes of /proc/net/dev. Injected to keep unit
// tests free of real filesystem dependencies.
type Source func() ([]byte, error)

// OperstateFunc returns the operstate string (e.g. "up", "down", "dormant")
// for the named interface. An error value indicates the interface is
// considered not-up; callers should not distinguish error kinds.
type OperstateFunc func(name string) (string, error)

// Clock returns the current wall-clock time. Injected for deterministic
// unit tests.
type Clock func() time.Time

// Collector samples interface counters on a ticker and publishes snapshots.
type Collector struct {
	ifaces    []string
	source    Source
	operstate OperstateFunc
	clock     Clock
	interval  time.Duration

	// prev holds the previous raw sample per interface. Only mutated from
	// the collector goroutine; never exposed.
	prev map[string]sample

	// snap is the atomically-published snapshot pointer. Readers load it
	// with a single atomic op; writers swap the pointer after building a
	// fully-constructed replacement.
	snap atomic.Pointer[Snapshot]
}

// New constructs a Collector for the interfaces named in cfg (hotspot,
// WAN, and admin). Empty names are skipped. The returned Collector uses
// cfg.ProcPath and cfg.SysPath for production filesystem access.
func New(cfg config.Config) *Collector {
	procFile := filepath.Join(cfg.ProcPath, "net", "dev")
	source := func() ([]byte, error) {
		return os.ReadFile(procFile)
	}
	operstate := func(name string) (string, error) {
		p := filepath.Join(cfg.SysPath, "class", "net", name, "operstate")
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return newWithDeps(
		filterEmpty([]string{cfg.HotspotIf, cfg.WANIf, cfg.AdminIf}),
		source,
		operstate,
		time.Now,
		sampleInterval,
	)
}

// newWithDeps is the test-friendly constructor: all filesystem and clock
// dependencies are injected. Production code uses New.
func newWithDeps(ifaces []string, source Source, operstate OperstateFunc, clock Clock, interval time.Duration) *Collector {
	c := &Collector{
		ifaces:    ifaces,
		source:    source,
		operstate: operstate,
		clock:     clock,
		interval:  interval,
		prev:      make(map[string]sample, len(ifaces)),
	}
	// Publish an empty initial snapshot so early readers never see nil.
	empty := &Snapshot{
		At:         clock(),
		Interfaces: initialInterfaces(ifaces),
	}
	c.snap.Store(empty)
	return c
}

// Run blocks, sampling every interval until ctx is done. It is intended
// to be invoked in its own goroutine by main.
func (c *Collector) Run(ctx context.Context) {
	log.Printf("netstats: collector starting (interval=%s ifaces=%v)", c.interval, c.ifaces)

	// Seed an initial sample immediately so the first tick produces a
	// valid delta rather than another zero-rate snapshot.
	c.tick()

	t := time.NewTicker(c.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("netstats: collector stopped")
			return
		case <-t.C:
			c.tick()
		}
	}
}

// Snapshot returns the most recently published snapshot. The returned
// pointer is safe for concurrent read-only access; callers must not
// mutate the map or its entries.
func (c *Collector) Snapshot() *Snapshot {
	return c.snap.Load()
}

// tick performs one sample cycle: read the source, compute rates against
// the previous sample, update prev, and publish a new snapshot.
func (c *Collector) tick() {
	now := c.clock()

	raw, err := c.source()
	current := map[string]sample{}
	if err == nil {
		parsed := parseProcNetDev(raw)
		for _, name := range c.ifaces {
			if s, ok := parsed[name]; ok {
				current[name] = sample{
					rxBytes: s.rxBytes,
					txBytes: s.txBytes,
					at:      now,
					present: true,
				}
			} else {
				current[name] = sample{at: now, present: false}
			}
		}
	} else {
		// On read error, mark every interface as absent; rates go to 0
		// but the snapshot timestamp still advances.
		for _, name := range c.ifaces {
			current[name] = sample{at: now, present: false}
		}
	}

	ifaces := make(map[string]InterfaceStats, len(c.ifaces))
	for _, name := range c.ifaces {
		cur := current[name]
		prev, hadPrev := c.prev[name]

		stats := InterfaceStats{
			Name:         name,
			Up:           c.isUp(name),
			RxTotalBytes: cur.rxBytes,
			TxTotalBytes: cur.txBytes,
		}
		if cur.present && hadPrev && prev.present {
			stats.RxMbps = computeMbps(prev.rxBytes, cur.rxBytes, prev.at, cur.at)
			stats.TxMbps = computeMbps(prev.txBytes, cur.txBytes, prev.at, cur.at)
		}
		ifaces[name] = stats
	}

	c.prev = current
	c.snap.Store(&Snapshot{At: now, Interfaces: ifaces})
}

// isUp returns true iff the kernel reports the interface's operstate as
// "up". Any other state (including "dormant", "unknown", "down", or a
// read error) is treated as not-up. This matches M2's locked decision
// to use operstate rather than the stricter carrier signal.
func (c *Collector) isUp(name string) bool {
	state, err := c.operstate(name)
	if err != nil {
		return false
	}
	return state == "up"
}

// computeMbps converts a byte-counter delta over an elapsed interval into
// megabits per second. A non-positive elapsed interval or a counter that
// has moved backward (interface reset) yields 0 rather than a bogus value.
func computeMbps(prevBytes, curBytes uint64, prevAt, curAt time.Time) float64 {
	elapsed := curAt.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	if curBytes < prevBytes {
		// Interface reassociated or counters reset; can't compute a rate.
		return 0
	}
	delta := curBytes - prevBytes
	return float64(delta) * 8 / 1_000_000 / elapsed
}

// parsedLine is the subset of columns we care about from /proc/net/dev.
type parsedLine struct {
	rxBytes uint64
	txBytes uint64
}

// parseProcNetDev extracts rx/tx byte counters per interface from the
// raw contents of /proc/net/dev. Malformed or non-interface lines are
// skipped silently.
//
// File format (16 numeric columns after the "iface:" prefix):
//
//	Inter-|   Receive                                                |  Transmit
//	 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
//	 wlan0:  12345    100    0    0    0     0          0         0     6789    50     0    0    0     0       0          0
func parseProcNetDev(raw []byte) map[string]parsedLine {
	out := make(map[string]parsedLine)
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue // header or malformed
		}
		name := strings.TrimSpace(line[:colon])
		if name == "" {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 16 {
			continue
		}
		var rx, tx uint64
		if _, err := fmt.Sscanf(fields[0], "%d", &rx); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(fields[8], "%d", &tx); err != nil {
			continue
		}
		out[name] = parsedLine{rxBytes: rx, txBytes: tx}
	}
	return out
}

// filterEmpty returns s with empty strings removed, preserving order.
func filterEmpty(s []string) []string {
	out := s[:0:0]
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// initialInterfaces returns a map with zero-valued entries for every
// interface name. Used to seed the first published snapshot so readers
// observe the expected keys immediately.
func initialInterfaces(ifaces []string) map[string]InterfaceStats {
	out := make(map[string]InterfaceStats, len(ifaces))
	for _, name := range ifaces {
		out[name] = InterfaceStats{Name: name}
	}
	return out
}
