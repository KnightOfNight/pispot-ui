package authz

import (
	"context"
	"fmt"
	"log"
	"net/http"
)

// contextKey is used to attach the authenticated role to the request context.
type contextKey string

const roleKey contextKey = "pispot-role"

// Middleware returns an HTTP middleware that enforces Basic Auth on all
// routes except /healthz. Authentication is delegated to the pispot-authd
// Unix socket at socketPath.
//
// Behavior:
//   - /healthz is always allowed unauthenticated.
//   - No Authorization header → 401.
//   - Socket unavailable → 503.
//   - Bad credentials or unauthorized group → 401.
//   - Success → role attached to context, handler called.
//
// When socketPath is empty the middleware is a no-op (local dev mode).
func Middleware(socketPath, realm string) func(http.Handler) http.Handler {
	if socketPath == "" {
		// No AUTH_SOCKET configured — auth disabled for local dev.
		return func(next http.Handler) http.Handler { return next }
	}

	cache := newAuthCache()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// /healthz is unauthenticated by design (liveness probe).
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}

			username, password, ok := r.BasicAuth()
			if !ok || username == "" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q`, realm))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Cache hit: skip socket call.
			if role, hit := cache.get(username, password); hit {
				ctx := context.WithValue(r.Context(), roleKey, role)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Cache miss: call pispot-authd.
			// Password is never logged.
			log.Printf("authz: cache miss for user=%q, calling helper", username)
			resp, err := callHelper(r.Context(), socketPath, username, password)
			if err != nil {
				// Socket unavailable: 503 so the operator knows the
				// helper is down, rather than silently failing with 401.
				log.Printf("authz: helper unavailable for user=%q: %v", username, err)
				http.Error(w, "Authentication service unavailable", http.StatusServiceUnavailable)
				return
			}

			if !resp.Ok {
				log.Printf("authz: denied user=%q: %s", username, resp.Error)
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q`, realm))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			log.Printf("authz: ok user=%q role=%s (cached for 5m)", username, resp.Role)
			cache.set(username, password, resp.Role)
			ctx := context.WithValue(r.Context(), roleKey, resp.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RoleFromContext returns the pispot role ("readonly" or "admin") attached
// to the request context by the auth middleware. Returns "" when auth is
// disabled (local dev) or the route is unauthenticated (/healthz).
func RoleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(roleKey).(string)
	return v
}
