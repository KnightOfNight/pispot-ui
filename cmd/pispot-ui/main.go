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

	"github.com/mcs-net/pispot-ui/internal/admin"
	"github.com/mcs-net/pispot-ui/internal/api"
	"github.com/mcs-net/pispot-ui/internal/buildinfo"
	"github.com/mcs-net/pispot-ui/internal/config"
	"github.com/mcs-net/pispot-ui/internal/hotspot"
	"github.com/mcs-net/pispot-ui/internal/netstats"
	"github.com/mcs-net/pispot-ui/internal/wan"
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

	// Hotspot, WAN, and admin collectors are lazy — no goroutines;
	// each refreshes on demand with its own TTL and exec timeout.
	hs := hotspot.New(cfg)
	wn := wan.New(cfg)
	ad := admin.New(cfg)

	srv := api.New(cfg, ns, hs, wn, ad)

	mux := http.NewServeMux()
	mux.Handle("/", noCacheStatic(http.FileServer(http.FS(web.FS()))))
	mux.HandleFunc("/api/stats", srv.Stats())
	mux.HandleFunc("/healthz", srv.Healthz())

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("pispot-ui %s (%s) listening on %s (hotspot=%s wan=%s admin=%s)",
			buildinfo.Commit, buildinfo.BuildTime,
			cfg.ListenAddr, cfg.HotspotIf, cfg.WANIf, cfg.AdminIf)
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

// noCacheStatic sets Cache-Control: no-cache, must-revalidate on every
// response so browsers always revalidate embedded assets (index.html,
// app.js, style.css) before reusing a cached copy. Go's http.FileServer
// emits ETag/Last-Modified for embedded files, so revalidation usually
// completes with a cheap 304 Not Modified when the content is unchanged.
//
// This prevents stale frontend behavior after a deploy — a class of bug
// that is otherwise silent and hard to diagnose.
func noCacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		h.ServeHTTP(w, r)
	})
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
