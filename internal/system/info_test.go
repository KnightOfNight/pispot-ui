package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Test 1 — parsers: loadavg, meminfo, temp. Covers happy paths and a
// handful of malformed-input cases to ensure we don't panic.
func TestParsers(t *testing.T) {
	// loadavg: five fields, first three are the ones we keep.
	l1, l5, l15, err := parseLoadavg([]byte("0.45 0.38 0.32 1/234 5678\n"))
	if err != nil {
		t.Fatalf("loadavg: unexpected error: %v", err)
	}
	if l1 != 0.45 || l5 != 0.38 || l15 != 0.32 {
		t.Errorf("loadavg: got %v/%v/%v, want 0.45/0.38/0.32", l1, l5, l15)
	}
	if _, _, _, err := parseLoadavg([]byte("short\n")); err == nil {
		t.Errorf("loadavg: expected error on short input")
	}
	if _, _, _, err := parseLoadavg([]byte("garbage garbage garbage\n")); err == nil {
		t.Errorf("loadavg: expected error on non-numeric input")
	}

	// meminfo: realistic fixture with MemTotal, MemFree, MemAvailable
	// plus several unrelated fields. used = (total - avail) * 1024.
	memFixture := `MemTotal:        8117380 kB
MemFree:         4123456 kB
MemAvailable:    6789012 kB
Buffers:          123456 kB
Cached:          1234567 kB
SwapTotal:             0 kB
`
	total, used, err := parseMeminfo([]byte(memFixture))
	if err != nil {
		t.Fatalf("meminfo: unexpected error: %v", err)
	}
	wantTotal := uint64(8117380) * 1024
	wantUsed := uint64(8117380-6789012) * 1024
	if total != wantTotal {
		t.Errorf("meminfo total: got %d, want %d", total, wantTotal)
	}
	if used != wantUsed {
		t.Errorf("meminfo used: got %d, want %d", used, wantUsed)
	}
	// MemAvailable missing → error.
	if _, _, err := parseMeminfo([]byte("MemTotal: 100 kB\n")); err == nil {
		t.Errorf("meminfo: expected error when MemAvailable absent")
	}
	// MemTotal missing → error.
	if _, _, err := parseMeminfo([]byte("MemAvailable: 100 kB\n")); err == nil {
		t.Errorf("meminfo: expected error when MemTotal absent")
	}

	// temp: millidegrees Celsius, integer.
	celsius, err := parseTempMillideg([]byte("47318\n"))
	if err != nil {
		t.Fatalf("temp: unexpected error: %v", err)
	}
	if celsius != 47.318 {
		t.Errorf("temp: got %v, want 47.318", celsius)
	}
	if _, err := parseTempMillideg([]byte("")); err == nil {
		t.Errorf("temp: expected error on empty input")
	}
	if _, err := parseTempMillideg([]byte("not a number\n")); err == nil {
		t.Errorf("temp: expected error on non-numeric input")
	}
}

// Test 2 — thermal zone auto-detection preference order.
// Use the real filesystem with a temporary sysfs-shaped tree.
func TestSelectThermalZone(t *testing.T) {
	makeZone := func(root, name, typ string) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "type"), []byte(typ+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "temp"), []byte("42000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("prefers cpu-thermal", func(t *testing.T) {
		root := t.TempDir()
		makeZone(root, "thermal_zone0", "bcm2712_thermal")
		makeZone(root, "thermal_zone1", "cpu-thermal")
		makeZone(root, "thermal_zone2", "gpu-thermal")
		got, err := selectThermalZone(root)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(root, "thermal_zone1", "temp")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to bcm2712_thermal when no cpu-thermal", func(t *testing.T) {
		root := t.TempDir()
		makeZone(root, "thermal_zone0", "bcm2712_thermal")
		makeZone(root, "thermal_zone1", "gpu-thermal")
		got, err := selectThermalZone(root)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(root, "thermal_zone0", "temp")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to any zone containing cpu", func(t *testing.T) {
		root := t.TempDir()
		makeZone(root, "thermal_zone0", "gpu-thermal")
		makeZone(root, "thermal_zone1", "CPU-something-custom")
		got, err := selectThermalZone(root)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(root, "thermal_zone1", "temp")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("final fallback to thermal_zone0", func(t *testing.T) {
		root := t.TempDir()
		makeZone(root, "thermal_zone0", "somethingelse")
		got, err := selectThermalZone(root)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(root, "thermal_zone0", "temp")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no zones at all -> error", func(t *testing.T) {
		root := t.TempDir()
		if _, err := selectThermalZone(root); err == nil {
			t.Errorf("expected error when no zones exist")
		}
	})
}

// Test 3 — Collector ticker: snapshot populates from injected readers,
// concurrent Snapshot() calls are race-free, and the goroutine stops on
// context cancel. Also verifies throttled flag flips at 80 °C.
func TestCollectorTick(t *testing.T) {
	var tempMilliDeg atomic.Int64
	tempMilliDeg.Store(47318) // 47.318 °C

	readLoad := func() ([]byte, error) {
		return []byte("0.10 0.20 0.30 1/100 9999\n"), nil
	}
	readMem := func() ([]byte, error) {
		return []byte("MemTotal: 8117380 kB\nMemAvailable: 6789012 kB\n"), nil
	}
	readTemp := func() ([]byte, error) {
		return []byte(fmt.Sprintf("%d\n", tempMilliDeg.Load())), nil
	}
	selectTherm := func() (string, error) { return "/fake/path/temp", nil }

	c := newWithDeps(readLoad, readMem, readTemp, selectTherm, time.Now, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	// Hammer Snapshot() concurrently while ticks run.
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
			}
		}()
	}

	// Give a few ticks to land under normal (not throttled) temperature.
	time.Sleep(50 * time.Millisecond)
	snap := c.Snapshot()
	if snap.Info.Load1m != 0.10 || snap.Info.Load5m != 0.20 || snap.Info.Load15m != 0.30 {
		t.Errorf("load: got %+v, want 0.10/0.20/0.30",
			[]float64{snap.Info.Load1m, snap.Info.Load5m, snap.Info.Load15m})
	}
	if snap.Info.MemTotalBytes == 0 || snap.Info.MemUsedBytes == 0 {
		t.Errorf("mem: got zero values: %+v", snap.Info)
	}
	if snap.Info.TempCelsius < 47.3 || snap.Info.TempCelsius > 47.4 {
		t.Errorf("temp: got %v, want ~47.3", snap.Info.TempCelsius)
	}
	if snap.Info.Throttled {
		t.Errorf("throttled: expected false at 47.3 °C")
	}

	// Bump temperature above the 80 °C threshold and verify flip.
	tempMilliDeg.Store(82500) // 82.5 °C
	time.Sleep(50 * time.Millisecond)
	snap = c.Snapshot()
	if !snap.Info.Throttled {
		t.Errorf("throttled: expected true at 82.5 °C, got %+v", snap.Info)
	}

	close(stop)
	wg.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("collector did not stop within 500 ms of cancel")
	}

	// Partial-failure path: one reader returns an error, others succeed.
	// Resulting snapshot must still populate the working fields and
	// surface the error. Use a fresh temp reader pinned to a known
	// value so this sub-case is independent of earlier state.
	staticTemp := func() ([]byte, error) { return []byte("47318\n"), nil }
	c2 := newWithDeps(
		readLoad,
		func() ([]byte, error) { return nil, errors.New("meminfo boom") },
		staticTemp,
		selectTherm,
		time.Now,
		10*time.Millisecond,
	)
	c2.tick()
	s := c2.Snapshot()
	if s.Err == nil {
		t.Errorf("expected composite error when a reader fails")
	}
	if s.Info.Load1m != 0.10 {
		t.Errorf("load should populate even when mem fails: got %v", s.Info.Load1m)
	}
	if s.Info.TempCelsius < 47.3 || s.Info.TempCelsius > 47.4 {
		t.Errorf("temp should populate even when mem fails: got %v", s.Info.TempCelsius)
	}
	if s.Info.MemTotalBytes != 0 {
		t.Errorf("mem total should remain zero when reader failed: got %d", s.Info.MemTotalBytes)
	}
}
