package wan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
)

const (
	cacheTTL    = 1 * time.Second
	execTimeout = 2 * time.Second
)

// Snapshot is the cached WAN state plus the error from the most recent
// refresh attempt (or nil on success). Info reflects the last successful
// refresh when Err is non-nil.
type Snapshot struct {
	At   time.Time
	Info Info
	Err  error
}

// execFunc runs a single command (iw or ip) with the supplied args and
// returns stdout. Injected for tests.
type execFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Collector lazily refreshes a cached WAN snapshot.
type Collector struct {
	iface string
	run   execFunc
	clock func() time.Time
	ttl   time.Duration

	mu     sync.Mutex
	lastAt time.Time
	snap   atomic.Pointer[Snapshot]
}

// New returns a Collector configured from cfg. Production use shells
// out to iw and ip with a per-invocation 2 s timeout.
func New(cfg config.Config) *Collector {
	return newWithDeps(cfg.WANIf, defaultRun, time.Now, cacheTTL)
}

func newWithDeps(iface string, run execFunc, clock func() time.Time, ttl time.Duration) *Collector {
	c := &Collector{
		iface: iface,
		run:   run,
		clock: clock,
		ttl:   ttl,
	}
	c.snap.Store(&Snapshot{At: clock(), Info: Info{Interface: iface}})
	return c
}

// Snapshot returns the cached snapshot, refreshing it if older than TTL.
func (c *Collector) Snapshot(ctx context.Context) *Snapshot {
	c.mu.Lock()
	age := c.clock().Sub(c.lastAt)
	stale := age >= c.ttl
	c.mu.Unlock()
	if stale {
		c.refresh(ctx)
	}
	return c.snap.Load()
}

// refresh runs iw link, and (if connected) ip addr + ip route, merging
// the results into a new Snapshot. Any command error results in a
// last-good snapshot with Err populated.
func (c *Collector) refresh(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clock().Sub(c.lastAt) < c.ttl {
		return
	}
	now := c.clock()
	prev := c.snap.Load()

	runCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	// Step 1: iw dev <iface> link — determines connection state + radio info.
	iwOut, iwErr := c.run(runCtx, "iw", "dev", c.iface, "link")
	if iwErr != nil {
		c.storeFailure(now, prev, fmt.Errorf("iw: %w", iwErr))
		return
	}
	link := parseIwLink(iwOut)

	info := Info{Interface: c.iface, Connected: link.connected}
	if !link.connected {
		// Locked M4 decision: disconnected clears every downstream field
		// so the UI never displays stale IP/BSSID data for a link that
		// is definitively not associated.
		c.storeSuccess(now, info)
		return
	}
	info.SSID = link.ssid
	info.BSSID = link.bssid
	info.SignalDBm = link.signalDBm
	info.FreqMHz = link.freqMHz
	info.TxBitrateMbps = link.txBitrateMbps

	// Step 2: ip -j addr show <iface> — primary IPv4 address.
	addrOut, addrErr := c.run(runCtx, "ip", "-j", "addr", "show", c.iface)
	if addrErr != nil {
		c.storeFailure(now, prev, fmt.Errorf("ip addr: %w", addrErr))
		return
	}
	info.IP = parseIPAddr(addrOut)

	// Step 3: ip -j route show default — default gateway for this iface.
	routeOut, routeErr := c.run(runCtx, "ip", "-j", "route", "show", "default")
	if routeErr != nil {
		c.storeFailure(now, prev, fmt.Errorf("ip route: %w", routeErr))
		return
	}
	info.Gateway = parseIPRoute(routeOut, c.iface)

	c.storeSuccess(now, info)
}

func (c *Collector) storeSuccess(now time.Time, info Info) {
	c.snap.Store(&Snapshot{At: now, Info: info})
	c.lastAt = now
}

func (c *Collector) storeFailure(now time.Time, prev *Snapshot, err error) {
	info := Info{Interface: c.iface}
	if prev != nil {
		info = prev.Info
	}
	c.snap.Store(&Snapshot{At: now, Info: info, Err: err})
	c.lastAt = now
}

// defaultRun is the production execFunc.
func defaultRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- name and args are hard-coded in the collector.
	return exec.CommandContext(ctx, name, args...).Output()
}

// Sentinel for tests: returned when a fake should not have been called.
var errUnexpectedCall = errors.New("unexpected command invocation")
