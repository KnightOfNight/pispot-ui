// Command pispot-ui serves the pispot dashboard web UI and JSON API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mcs-net/pispot-ui/internal/api"
	"github.com/mcs-net/pispot-ui/internal/config"
	"github.com/mcs-net/pispot-ui/internal/hotspot"
	"github.com/mcs-net/pispot-ui/internal/netstats"
	"github.com/mcs-net/pispot-ui/internal/web"
)

func main() {
	cfg := config.Load()

	// Graceful shutdown on SIGINT/SIGTERM. The context drives both the
	// netstats collector goroutine and the HTTP server's Shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the netstats collector in its own goroutine. It publishes
	// snapshots that the API handlers read on demand.
	ns := netstats.New(cfg)
	go ns.Run(ctx)

	// Hotspot collector is lazy — no goroutine; refreshed on demand
	// with its own TTL and exec timeout.
	hs := hotspot.New(cfg)

	srv := api.New(cfg, ns, hs)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(web.FS())))
	mux.HandleFunc("/api/stats", srv.Stats())
	mux.HandleFunc("/healthz", srv.Healthz())

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("pispot-ui %s listening on %s (hotspot=%s wan=%s admin=%s)",
			cfg.Version, cfg.ListenAddr, cfg.HotspotIf, cfg.WANIf, cfg.AdminIf)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
		os.Exit(1)
	}
}

// logRequests emits one line per request with method, path, status, and duration.
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(ww, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, ww.status, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
