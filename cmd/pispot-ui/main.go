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
	"github.com/mcs-net/pispot-ui/internal/authz"
	"github.com/mcs-net/pispot-ui/internal/buildinfo"
	"github.com/mcs-net/pispot-ui/internal/config"
	"github.com/mcs-net/pispot-ui/internal/hotspot"
	"github.com/mcs-net/pispot-ui/internal/netstats"
	"github.com/mcs-net/pispot-ui/internal/system"
	"github.com/mcs-net/pispot-ui/internal/wan"
	"github.com/mcs-net/pispot-ui/internal/web"
)

func main() {
	cfg := config.Load()

	// Graceful shutdown on SIGINT/SIGTERM. The context drives both the
	// netstats collector goroutine and the HTTP server's Shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the netstats and system collectors in their own goroutines.
	// Both publish snapshots via atomic pointer swap; API handlers
	// read them lock-free.
	ns := netstats.New(cfg)
	go ns.Run(ctx)
	sys := system.New(cfg)
	go sys.Run(ctx)

	// Hotspot, WAN, and admin collectors are lazy — no goroutines;
	// each refreshes on demand with its own TTL and exec timeout.
	hs := hotspot.New(cfg)
	wn := wan.New(cfg)
	ad := admin.New(cfg)

	srv := api.New(cfg, ns, hs, wn, ad, sys)

	mux := http.NewServeMux()
	mux.Handle("/", noCacheStatic(http.FileServer(http.FS(web.FS()))))
	mux.HandleFunc("/api/stats", srv.Stats())
	mux.HandleFunc("/api/wan/up", srv.WanOp("wan_up"))
	mux.HandleFunc("/api/wan/down", srv.WanOp("wan_down"))
	mux.HandleFunc("/api/wifi/networks", srv.WifiNetworks())
	mux.HandleFunc("/api/wifi/networks/", srv.WifiNetwork())
	mux.HandleFunc("/api/wifi/reload", srv.WifiReload())
	mux.HandleFunc("/healthz", srv.Healthz())

	// Auth middleware: no-op when AUTH_SOCKET is unset (local dev).
	// When set, all routes except /healthz require Basic Auth via
	// pispot-authd. Returns 503 when the helper socket is unavailable.
	authMiddleware := authz.Middleware(cfg.AuthSocket, cfg.AuthRealm)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequests(authMiddleware(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	tlsEnabled := cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		log.Fatalf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}
	if cfg.RequireTLS && !tlsEnabled {
		log.Fatalf("REQUIRE_TLS is true but TLS_CERT_FILE/TLS_KEY_FILE are not both set")
	}

	go func() {
		scheme := "http"
		if tlsEnabled {
			scheme = "https"
		}
		log.Printf("pispot-ui %s (%s) listening on %s://%s (hotspot=%s wan=%s admin=%s)",
			buildinfo.Commit, buildinfo.BuildTime,
			scheme, cfg.ListenAddr, cfg.HotspotIf, cfg.WANIf, cfg.AdminIf)
		log.Printf("tls: enabled=%v cert=%s", tlsEnabled, cfg.TLSCertFile)
		if cfg.AuthSocket != "" {
			log.Printf("auth: socket=%s realm=%q", cfg.AuthSocket, cfg.AuthRealm)
		} else {
			log.Printf("auth: disabled (AUTH_SOCKET not set)")
		}
		var err error
		if tlsEnabled {
			err = httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
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
