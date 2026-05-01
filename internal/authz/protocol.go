// Package authz provides Basic Auth middleware for pispot-ui that
// delegates authentication and role resolution to the pispot-authd
// host helper over a Unix socket.
//
// The wire types below are intentionally duplicated from the OS repo
// (github.com/mcs-net/pispot-os/internal/socket) so both repos remain
// independently buildable without a shared module dependency.
package authz

// authRequest is sent to pispot-authd over the Unix socket.
type authRequest struct {
	Op       string `json:"op"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// authResponse is returned by pispot-authd.
// Role is "readonly" or "admin" when Ok is true.
type authResponse struct {
	Ok       bool   `json:"ok"`
	Username string `json:"username,omitempty"`
	Role     string `json:"role,omitempty"`
	Error    string `json:"error,omitempty"`
}
