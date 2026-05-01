package wan

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
)

const (
	cacheTTL    = 1 * time.Second
	execTimeout = 2 * time.Second
)

// ErrInterfaceAbsent is the error stored on Snapshot.Err when the
// configured WAN interface does not exist in sysfs (e.g. a USB Wi-Fi
// adapter not plugged in). Refresh short-circuits in that case so the
// dashboard never waits for `iw` to fail with ENODEV.
var ErrInterfaceAbsent = errors.New("interface absent")

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

// existsFunc reports whether the named interface exists. In production
// this is a sysfs stat; tests inject a stub.
type existsFunc func(name string) bool

// Collector lazily refreshes a cached WAN snapshot.
type Collector struct {
	iface  string
	run    execFunc
	exists existsFunc
	clock  func() time.Time
	ttl    time.Duration

	mu            sync.Mutex
	lastAt        time.Time
	snap          atomic.Pointer[Snapshot]
	prevConnected bool
	prevSSID      string
	prevAbsent    bool
}

// New returns a Collector configured from cfg. Production use shells
// out to iw and ip with a per-invocation 2 s timeout.
func New(cfg config.Config) *Collector {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(cfg.SysPath, "class", "net", name))
		return err == nil
	}
	return newWithDeps(cfg.WANIf, defaultRun, exists, time.Now, cacheTTL)
}

func newWithDeps(iface string, run execFunc, exists existsFunc, clock func() time.Time, ttl time.Duration) *Collector {
	c := &Collector{
		iface:  iface,
		run:    run,
		exists: exists,
		clock:  clock,
		ttl:    ttl,
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

	// Short-circuit when the configured interface is absent from sysfs
	// (e.g. USB Wi-Fi adapter not plugged in). Avoids a multi-second
	// wait for `iw` to fail with ENODEV on first request after a boot
	// without the device.
	if c.exists != nil && !c.exists(c.iface) {
		if !c.prevAbsent {
			log.Printf("wan: interface %s absent in sysfs", c.iface)
			c.prevAbsent = true
		}
		c.snap.Store(&Snapshot{
			At:   now,
			Info: Info{Interface: c.iface, InterfacePresent: false},
			Err:  ErrInterfaceAbsent,
		})
		c.lastAt = now
		return
	}
	if c.prevAbsent {
		log.Printf("wan: interface %s present in sysfs", c.iface)
		c.prevAbsent = false
	}

	runCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	// Step 1: iw dev <iface> link — determines connection state + radio info.
	iwOut, iwErr := c.run(runCtx, "iw", "dev", c.iface, "link")
	if iwErr != nil {
		log.Printf("wan: iw link failed on %s: %v", c.iface, iwErr)
		c.storeFailure(now, prev, fmt.Errorf("iw: %w", iwErr))
		return
	}
	link := parseIwLink(iwOut)

	info := Info{Interface: c.iface, InterfacePresent: true, Connected: link.connected}
	if !link.connected {
		if c.prevConnected {
			log.Printf("wan: %s disconnected (was connected to %q)", c.iface, c.prevSSID)
			c.prevConnected = false
			c.prevSSID = ""
		}
		// Locked M4 decision: disconnected clears every downstream field
		// so the UI never displays stale IP/BSSID data for a link that
		// is definitively not associated.
		c.storeSuccess(now, info)
		return
	}
	if !c.prevConnected || link.ssid != c.prevSSID {
		log.Printf("wan: %s connected ssid=%q signal=%ddBm freq=%dMHz", c.iface, link.ssid, link.signalDBm, link.freqMHz)
		c.prevConnected = true
		c.prevSSID = link.ssid
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
