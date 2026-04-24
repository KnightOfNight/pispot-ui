package hotspot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
)

// Default timings. cacheTTL bounds iw exec frequency even under rapid
// polling; execTimeout prevents a hung iw from wedging the handler.
const (
	cacheTTL    = 1 * time.Second
	execTimeout = 2 * time.Second
)

// Snapshot is the cached result of a successful (or last-good) refresh.
// It is published atomically; callers must treat it as read-only.
type Snapshot struct {
	At      time.Time
	Iface   string
	Clients []Client
	// Err is set when the most recent refresh attempt failed. The Clients
	// slice reflects the last successful refresh, so the UI can keep
	// displaying known-good data while also surfacing the error.
	Err error
}

// iwFunc runs `iw dev <iface> station dump` (or a test stand-in) and
// returns its stdout.
type iwFunc func(ctx context.Context, iface string) ([]byte, error)

// leasesFunc returns the contents of the dnsmasq lease file (or a test
// stand-in). A non-existent file should return an fs.ErrNotExist so
// callers can distinguish "no leases known" from "read error".
type leasesFunc func() ([]byte, error)

// Collector lazily refreshes a cached hotspot snapshot. Refresh happens
// at most once per cacheTTL; failed refreshes leave the previous
// snapshot's Clients intact but set Snapshot.Err.
type Collector struct {
	iface  string
	runIw  iwFunc
	leases leasesFunc
	clock  func() time.Time
	ttl    time.Duration

	mu     sync.Mutex
	lastAt time.Time
	snap   atomic.Pointer[Snapshot]
}

// New returns a Collector configured from cfg. Production use: iw is
// invoked via exec.CommandContext with a 2s timeout; the lease file is
// read at cfg.LeasesPath.
func New(cfg config.Config) *Collector {
	return newWithDeps(
		cfg.HotspotIf,
		defaultRunIw,
		func() ([]byte, error) { return os.ReadFile(cfg.LeasesPath) },
		time.Now,
		cacheTTL,
	)
}

// newWithDeps is the test-friendly constructor: all external effects
// are injected.
func newWithDeps(iface string, runIw iwFunc, leases leasesFunc, clock func() time.Time, ttl time.Duration) *Collector {
	c := &Collector{
		iface:  iface,
		runIw:  runIw,
		leases: leases,
		clock:  clock,
		ttl:    ttl,
	}
	// Publish an empty initial snapshot so readers never observe nil.
	c.snap.Store(&Snapshot{At: clock(), Iface: iface})
	return c
}

// Snapshot returns the cached snapshot, refreshing it if the cache is
// older than the configured TTL. ctx bounds the refresh; callers should
// pass the request context so a slow iw doesn't outlive the HTTP handler.
func (c *Collector) Snapshot(ctx context.Context) *Snapshot {
	c.mu.Lock()
	age := c.clock().Sub(c.lastAt)
	needRefresh := age >= c.ttl
	c.mu.Unlock()

	if needRefresh {
		c.refresh(ctx)
	}
	return c.snap.Load()
}

// refresh runs iw and merges the result with the dnsmasq leases. On
// success the snapshot's Clients and At advance and Err is cleared. On
// failure the previous Clients are retained and Err is populated.
func (c *Collector) refresh(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check under the lock to collapse concurrent refresh requests.
	if c.clock().Sub(c.lastAt) < c.ttl {
		return
	}

	now := c.clock()
	prev := c.snap.Load()

	iwCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	iwOut, iwErr := c.runIw(iwCtx, c.iface)
	if iwErr != nil {
		// Keep last-good Clients; surface the error.
		next := &Snapshot{
			At:      now,
			Iface:   c.iface,
			Clients: prev.Clients,
			Err:     fmt.Errorf("iw: %w", iwErr),
		}
		c.snap.Store(next)
		c.lastAt = now
		return
	}

	clients := parseStationDump(iwOut)

	// Enrich with dnsmasq lease data. A missing lease file is not fatal —
	// we still return the stations, just without IPs or hostnames.
	leasesRaw, leaseErr := c.leases()
	if leaseErr == nil {
		leases := parseLeases(leasesRaw)
		for i := range clients {
			if entry, ok := leases[clients[i].MAC]; ok {
				clients[i].IP = entry.IP
				clients[i].Hostname = entry.Hostname
			}
		}
	}

	// Stable, alphabetic-by-MAC ordering so UI rows don't jump between
	// refreshes.
	sort.Slice(clients, func(i, j int) bool {
		return clients[i].MAC < clients[j].MAC
	})

	next := &Snapshot{
		At:      now,
		Iface:   c.iface,
		Clients: clients,
	}
	if leaseErr != nil && !errors.Is(leaseErr, os.ErrNotExist) {
		// Missing lease file is normal (e.g. no DHCP clients yet); only
		// surface other read errors (permissions, I/O).
		next.Err = fmt.Errorf("leases: %w", leaseErr)
	}
	c.snap.Store(next)
	c.lastAt = now
}

// defaultRunIw is the production iwFunc: execs `iw dev <iface> station dump`.
func defaultRunIw(ctx context.Context, iface string) ([]byte, error) {
	// #nosec G204 -- iface comes from our own config, not user input.
	cmd := exec.CommandContext(ctx, "iw", "dev", iface, "station", "dump")
	return cmd.Output()
}
