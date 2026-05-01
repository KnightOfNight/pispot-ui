// Package system collects host-level metrics suitable for a small
// system-status pane: load average, memory, SoC temperature, and an
// inferred thermal-throttling flag.
//
// All readings come from /proc and /sys — no exec, no capabilities
// beyond the existing read-only /host/proc and /host/sys bind mounts.
// A background goroutine samples every sampleInterval and publishes a
// snapshot via atomic pointer swap; callers read it lock-free.
package system

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mcs-net/pispot-ui/internal/config"
)

// sampleInterval is the fixed cadence at which system metrics are read.
// The underlying files are cheap to parse; 1 s matches netstats.
const sampleInterval = 1 * time.Second

// thermalThrottleCelsius is the temperature at or above which the
// "throttled" flag is set. The Pi 5 firmware soft-throttles at 80 °C;
// we treat that as the inferred boundary here because we cannot read
// the firmware throttle flag directly without vcgencmd.
const thermalThrottleCelsius = 80.0

// Info is the public, per-snapshot view of host state.
type Info struct {
	Load1m        float64
	Load5m        float64
	Load15m       float64
	MemTotalBytes uint64
	MemUsedBytes  uint64
	TempCelsius   float64
	Throttled     bool
}

// Snapshot is a point-in-time view plus any error encountered during
// the most recent refresh. Err non-nil does not prevent callers from
// using Info; fields that could not be read are left zero-valued.
type Snapshot struct {
	At   time.Time
	Info Info
	Err  error
}

// readerFunc returns raw file contents. Injected to keep unit tests
// free of real filesystem dependencies.
type readerFunc func() ([]byte, error)

// thermalSelector returns the absolute path to the thermal-zone "temp"
// file that should be used as the SoC temperature source. The selection
// logic prefers cpu-* / bcm2712_* zones and falls back to thermal_zone0.
type thermalSelector func() (string, error)

// Collector samples /proc and /sys on a ticker and publishes snapshots.
type Collector struct {
	readLoad    readerFunc
	readMem     readerFunc
	readTemp    readerFunc
	selectTherm thermalSelector // invoked on each tick to tolerate late-appearing sensors
	clock       func() time.Time
	interval    time.Duration
	snap        atomic.Pointer[Snapshot]
}

// New returns a Collector configured from cfg. In production it reads
// /host/proc/loadavg, /host/proc/meminfo, and the auto-detected thermal
// zone under /host/sys/class/thermal.
func New(cfg config.Config) *Collector {
	loadPath := filepath.Join(cfg.ProcPath, "loadavg")
	memPath := filepath.Join(cfg.ProcPath, "meminfo")
	thermalRoot := filepath.Join(cfg.SysPath, "class", "thermal")

	selectTherm := func() (string, error) {
		return selectThermalZone(thermalRoot)
	}

	readTemp := func() ([]byte, error) {
		path, err := selectTherm()
		if err != nil {
			return nil, err
		}
		return os.ReadFile(path)
	}

	return newWithDeps(
		func() ([]byte, error) { return os.ReadFile(loadPath) },
		func() ([]byte, error) { return os.ReadFile(memPath) },
		readTemp,
		selectTherm,
		time.Now,
		sampleInterval,
	)
}

// newWithDeps is the test-friendly constructor: every external effect
// is injected.
func newWithDeps(readLoad, readMem, readTemp readerFunc, selectTherm thermalSelector, clock func() time.Time, interval time.Duration) *Collector {
	c := &Collector{
		readLoad:    readLoad,
		readMem:     readMem,
		readTemp:    readTemp,
		selectTherm: selectTherm,
		clock:       clock,
		interval:    interval,
	}
	// Publish an empty initial snapshot so early readers never see nil.
	c.snap.Store(&Snapshot{At: clock()})
	return c
}

// Run blocks, sampling every interval until ctx is done. Intended to
// run in its own goroutine.
func (c *Collector) Run(ctx context.Context) {
	log.Printf("system: collector starting (interval=%s)", c.interval)

	// Seed an initial sample immediately.
	c.tick()

	t := time.NewTicker(c.interval)
	defer t.Stop()

	var prevThrottled bool
	for {
		select {
		case <-ctx.Done():
			log.Printf("system: collector stopped")
			return
		case <-t.C:
			c.tick()
			// Log only on throttle state changes to avoid 86400 lines/day.
			snap := c.Snapshot()
			if snap != nil && snap.Info.Throttled != prevThrottled {
				if snap.Info.Throttled {
					log.Printf("system: THROTTLED temp=%.1f°C (threshold=%.0f°C)",
						snap.Info.TempCelsius, thermalThrottleCelsius)
				} else {
					log.Printf("system: throttle cleared temp=%.1f°C", snap.Info.TempCelsius)
				}
				prevThrottled = snap.Info.Throttled
			}
		}
	}
}

// Snapshot returns the most recently published snapshot.
func (c *Collector) Snapshot() *Snapshot {
	return c.snap.Load()
}

// tick performs one sample cycle. Errors from individual readers are
// collected into a single composite error; fields from readers that
// failed remain zero-valued.
func (c *Collector) tick() {
	now := c.clock()
	var info Info
	var errs []string

	if raw, err := c.readLoad(); err != nil {
		errs = append(errs, fmt.Sprintf("loadavg: %v", err))
	} else {
		l1, l5, l15, err := parseLoadavg(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("loadavg: %v", err))
		} else {
			info.Load1m, info.Load5m, info.Load15m = l1, l5, l15
		}
	}

	if raw, err := c.readMem(); err != nil {
		errs = append(errs, fmt.Sprintf("meminfo: %v", err))
	} else {
		total, used, err := parseMeminfo(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("meminfo: %v", err))
		} else {
			info.MemTotalBytes = total
			info.MemUsedBytes = used
		}
	}

	if raw, err := c.readTemp(); err != nil {
		errs = append(errs, fmt.Sprintf("temp: %v", err))
	} else {
		temp, err := parseTempMillideg(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("temp: %v", err))
		} else {
			info.TempCelsius = temp
			info.Throttled = temp >= thermalThrottleCelsius
		}
	}

	snap := &Snapshot{At: now, Info: info}
	if len(errs) > 0 {
		snap.Err = fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	c.snap.Store(snap)
}

// parseLoadavg extracts the three load averages from /proc/loadavg.
// Format: "1m 5m 15m running/total last_pid".
func parseLoadavg(raw []byte) (l1, l5, l15 float64, err error) {
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("expected 3+ fields, got %d", len(fields))
	}
	l1, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("1m: %w", err)
	}
	l5, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("5m: %w", err)
	}
	l15, err = strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("15m: %w", err)
	}
	return l1, l5, l15, nil
}

// parseMeminfo extracts MemTotal and MemAvailable from /proc/meminfo
// and returns (total_bytes, used_bytes). used = total - available,
// matching what `free -h` reports as "used" in its "available" column
// era (kernel 3.14+). Values in /proc/meminfo are kB; we convert to
// bytes on the way out.
func parseMeminfo(raw []byte) (total, used uint64, err error) {
	var totalKB, availKB uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			if v, ok := parseMeminfoValue(line); ok {
				totalKB = v
				haveTotal = true
			}
		case strings.HasPrefix(line, "MemAvailable:"):
			if v, ok := parseMeminfoValue(line); ok {
				availKB = v
				haveAvail = true
			}
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal {
		return 0, 0, fmt.Errorf("MemTotal not found")
	}
	if !haveAvail {
		return 0, 0, fmt.Errorf("MemAvailable not found")
	}
	if availKB > totalKB {
		// Kernel bug or fixture oddity; clamp so math stays sane.
		availKB = totalKB
	}
	return totalKB * 1024, (totalKB - availKB) * 1024, nil
}

// parseMeminfoValue extracts the integer kB value from a meminfo line
// of the form "Name: 1234567 kB".
func parseMeminfoValue(line string) (uint64, bool) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return 0, false
	}
	fields := strings.Fields(line[colon+1:])
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseTempMillideg converts the contents of a thermal_zone*/temp file
// (integer millidegrees Celsius) to a floating-point Celsius value.
func parseTempMillideg(raw []byte) (float64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, fmt.Errorf("empty temperature reading")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return float64(n) / 1000.0, nil
}

// selectThermalZone returns the absolute path to the preferred thermal
// zone's temp file. Preference order:
//
//  1. Zone whose type == "cpu-thermal"
//  2. Zone whose type == "bcm2712_thermal"
//  3. Any zone whose type contains "cpu" (case-insensitive)
//  4. thermal_zone0 as a last-resort fallback
//
// An error is returned only when even thermal_zone0 is unreadable.
func selectThermalZone(root string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "thermal_zone*", "type"))
	if err == nil && len(matches) > 0 {
		var cpuContains string
		for _, m := range matches {
			b, rErr := os.ReadFile(m)
			if rErr != nil {
				continue
			}
			t := strings.TrimSpace(string(b))
			dir := filepath.Dir(m)
			temp := filepath.Join(dir, "temp")
			switch {
			case t == "cpu-thermal":
				return temp, nil
			case t == "bcm2712_thermal":
				// Note best-effort match; continue scanning in case a
				// "cpu-thermal" appears later and wins preference.
				if cpuContains == "" {
					cpuContains = temp
				}
			case strings.Contains(strings.ToLower(t), "cpu"):
				if cpuContains == "" {
					cpuContains = temp
				}
			}
		}
		if cpuContains != "" {
			return cpuContains, nil
		}
	}

	// Fallback: thermal_zone0 regardless of type.
	fallback := filepath.Join(root, "thermal_zone0", "temp")
	if _, err := os.Stat(fallback); err != nil {
		return "", fmt.Errorf("no usable thermal zone under %s: %w", root, err)
	}
	return fallback, nil
}
