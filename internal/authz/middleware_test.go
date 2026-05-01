package authz

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// startFakeHelper starts a Unix socket server that returns the provided
// response for every auth request. It returns the socket path and a
// teardown function.
func startFakeHelper(t *testing.T, resp authResponse) string {
	t.Helper()
	// Use /tmp directly — TempDir paths can exceed the 108-char Unix socket limit.
	path := filepath.Join(os.TempDir(), "pispot-fake-"+t.Name()+".sock")
	_ = os.Remove(path)

	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close(); os.Remove(path) })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req authRequest
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				_ = json.NewEncoder(c).Encode(resp)
			}(conn)
		}
	}()
	return path
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Test: /healthz bypasses auth entirely.
func TestHealthzUnauthenticated(t *testing.T) {
	mw := Middleware("/nonexistent.sock", "test")
	h := mw(http.HandlerFunc(okHandler))
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("healthz: got %d, want 200", w.Code)
	}
}

// Test: no Authorization header → 401 with WWW-Authenticate.
func TestNoAuthHeader(t *testing.T) {
	path := startFakeHelper(t, authResponse{Ok: true, Role: "readonly"})
	mw := Middleware(path, "test realm")
	h := mw(http.HandlerFunc(okHandler))
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no header: got %d, want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no header: expected WWW-Authenticate header")
	}
}

// Test: socket unavailable → 503.
func TestSocketUnavailable(t *testing.T) {
	mw := Middleware("/tmp/pispot-nonexistent-sock-test", "test")
	h := mw(http.HandlerFunc(okHandler))
	r := httptest.NewRequest("GET", "/api/stats", nil)
	r.SetBasicAuth("user", "pass")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("socket unavail: got %d, want 503", w.Code)
	}
}

// Test: bad credentials (helper returns ok=false) → 401.
func TestBadCredentials(t *testing.T) {
	path := startFakeHelper(t, authResponse{Ok: false, Error: "authentication failed"})
	mw := Middleware(path, "test")
	h := mw(http.HandlerFunc(okHandler))
	r := httptest.NewRequest("GET", "/api/stats", nil)
	r.SetBasicAuth("user", "wrongpassword")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad creds: got %d, want 401", w.Code)
	}
}

// Test: good credentials → 200, role attached to context.
func TestGoodCredentials(t *testing.T) {
	path := startFakeHelper(t, authResponse{Ok: true, Username: "ctg", Role: "admin"})
	mw := Middleware(path, "test")
	var gotRole string
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRole = RoleFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/api/stats", nil)
	r.SetBasicAuth("ctg", "correctpassword")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("good creds: got %d, want 200", w.Code)
	}
	if gotRole != "admin" {
		t.Errorf("good creds: got role %q, want admin", gotRole)
	}
}

// Test: auth cache — second request with same credentials skips helper.
// We verify this by counting how many times the helper socket is called:
// all 5 requests use the same credentials, so only 1 helper call is expected.
func TestAuthCacheSkipsHelper(t *testing.T) {
	var calls int
	var mu sync.Mutex

	// Use /tmp directly — TempDir paths can exceed the 108-char Unix socket limit.
	path := filepath.Join(os.TempDir(), "pispot-cache-test.sock")
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { l.Close(); <-done })

	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			calls++
			mu.Unlock()
			c := conn
			go func() {
				defer c.Close()
				var req authRequest
				_ = json.NewDecoder(c).Decode(&req)
				_ = json.NewEncoder(c).Encode(authResponse{Ok: true, Role: "readonly"})
			}()
		}
	}()

	mw := Middleware(path, "test")
	h := mw(http.HandlerFunc(okHandler))

	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", "/api/stats", nil)
		r.SetBasicAuth("ctg", "pass")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("cache: expected 1 helper call, got %d", got)
	}
}

// Test: auth cache TTL expiry causes re-authentication.
func TestAuthCacheExpiry(t *testing.T) {
	c := newAuthCache()
	c.items[cacheKey("user", "pass")] = cachedEntry{
		role:    "readonly",
		expires: time.Now().Add(-1 * time.Second), // already expired
	}
	if _, hit := c.get("user", "pass"); hit {
		t.Errorf("expired cache entry should not be a hit")
	}
}

// Test: empty socket path → no-op middleware (local dev mode).
func TestNoopWhenNoSocket(t *testing.T) {
	mw := Middleware("", "test")
	h := mw(http.HandlerFunc(okHandler))
	r := httptest.NewRequest("GET", "/api/stats", nil)
	// No auth header — should still pass in no-op mode.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("no-op: got %d, want 200", w.Code)
	}
}

// Test: RoleFromContext returns empty string when no role is attached.
func TestRoleFromContextEmpty(t *testing.T) {
	if r := RoleFromContext(context.Background()); r != "" {
		t.Errorf("empty context: got %q, want empty", r)
	}
}
