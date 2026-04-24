// Package admin collects link state and IP address for the administration
// interface (typically eth0). Link state comes from
// /sys/class/net/<iface>/operstate; the IP comes from `ip -j addr show`.
//
// Like the WAN collector, refreshes are lazy with a short TTL; failed
// refreshes retain the previous snapshot's Info and surface the error
// via Snapshot.Err.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
)

const (
	cacheTTL    = 1 * time.Second
	execTimeout = 2 * time.Second
)

// Info is the public, flattened view of the admin interface.
type Info struct {
	Interface string
	IP        string
	Gateway   string
	Link      bool
}

// Snapshot is the cached result plus refresh error (if any).
type Snapshot struct {
	At   time.Time
	Info Info
	Err  error
}

// operstateFunc reads /sys/class/net/<iface>/operstate (or a test fake)
// and returns the trimmed string contents.
type operstateFunc func(name string) (string, error)

// execFunc runs a single command and returns stdout. Injected for tests.
type execFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Collector lazily refreshes admin-interface state.
type Collector struct {
	iface     string
	operstate operstateFunc
	run       execFunc
	clock     func() time.Time
	ttl       time.Duration

	mu     sync.Mutex
	lastAt time.Time
	snap   atomic.Pointer[Snapshot]
}

// New returns a Collector configured from cfg. In production operstate
// is read from cfg.SysPath/class/net/<iface>/operstate; IP is fetched
// via `ip -j addr show <iface>`.
func New(cfg config.Config) *Collector {
	operstate := func(name string) (string, error) {
		p := filepath.Join(cfg.SysPath, "class", "net", name, "operstate")
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return newWithDeps(cfg.AdminIf, operstate, defaultRun, time.Now, cacheTTL)
}

func newWithDeps(iface string, op operstateFunc, run execFunc, clock func() time.Time, ttl time.Duration) *Collector {
	c := &Collector{
		iface:     iface,
		operstate: op,
		run:       run,
		clock:     clock,
		ttl:       ttl,
	}
	c.snap.Store(&Snapshot{At: clock(), Info: Info{Interface: iface}})
	return c
}

// Snapshot returns the cached snapshot, refreshing if older than TTL.
func (c *Collector) Snapshot(ctx context.Context) *Snapshot {
	c.mu.Lock()
	stale := c.clock().Sub(c.lastAt) >= c.ttl
	c.mu.Unlock()
	if stale {
		c.refresh(ctx)
	}
	return c.snap.Load()
}

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

	info := Info{Interface: c.iface}

	// Link state. An error reading operstate is surfaced but not fatal
	// to the IP lookup — the admin interface's IP can still be useful
	// even when the sysfs read fails for some reason.
	state, opErr := c.operstate(c.iface)
	info.Link = opErr == nil && state == "up"

	// IP address via `ip -j addr show <iface>`.
	addrOut, addrErr := c.run(runCtx, "ip", "-j", "addr", "show", c.iface)
	if addrErr != nil {
		// Retain last-good IP + gateway; surface combined error text.
		prior := prev.Info
		info.IP = prior.IP
		info.Gateway = prior.Gateway
		err := fmt.Errorf("ip addr: %w", addrErr)
		if opErr != nil {
			err = fmt.Errorf("%w; operstate: %v", err, opErr)
		}
		c.snap.Store(&Snapshot{At: now, Info: info, Err: err})
		c.lastAt = now
		return
	}
	info.IP = parseIPAddr(addrOut)

	// Default gateway for this interface via `ip -j route show default`.
	// Treated as non-fatal: if the route fetch fails we simply leave
	// Gateway blank rather than blocking the rest of the section.
	if routeOut, routeErr := c.run(runCtx, "ip", "-j", "route", "show", "default"); routeErr == nil {
		info.Gateway = parseIPRoute(routeOut, c.iface)
	}

	next := &Snapshot{At: now, Info: info}
	if opErr != nil {
		next.Err = fmt.Errorf("operstate: %w", opErr)
	}
	c.snap.Store(next)
	c.lastAt = now
}

// parseIPAddr extracts the first IPv4 address from `ip -j addr show`
// output. Duplicated here rather than shared with the WAN package to
// keep package boundaries clean; the body is trivial and the tests are
// correspondingly small.
func parseIPAddr(raw []byte) string {
	var doc []struct {
		AddrInfo []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, iface := range doc {
		for _, a := range iface.AddrInfo {
			if a.Family == "inet" && a.Local != "" {
				return a.Local
			}
		}
	}
	return ""
}

// parseIPRoute extracts the gateway of the first default route whose
// dev matches iface from `ip -j route show default` output. Returns ""
// when no such route exists. Duplicated from the wan package to keep
// package boundaries clean.
func parseIPRoute(raw []byte, iface string) string {
	var doc []struct {
		Dst     string `json:"dst"`
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, r := range doc {
		if r.Dev == iface && r.Gateway != "" {
			return r.Gateway
		}
	}
	return ""
}

// defaultRun is the production execFunc.
func defaultRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- name and args are hard-coded in the collector.
	return exec.CommandContext(ctx, name, args...).Output()
}
