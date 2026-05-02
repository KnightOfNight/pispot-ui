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
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// WiFi fields — used for wifi_add and wifi_remove ops.
	SSID string `json:"ssid,omitempty"`
	PSK  string `json:"psk,omitempty"`
}

// authResponse is returned by pispot-authd.
// Role is "readonly" or "admin" when Ok is true.
// Networks is populated by wifi_list responses.
type authResponse struct {
	Ok       bool          `json:"ok"`
	Username string        `json:"username,omitempty"`
	Role     string        `json:"role,omitempty"`
	Error    string        `json:"error,omitempty"`
	Networks []wifiNetwork `json:"networks,omitempty"`
}

// wifiNetwork is one network entry in a wifi_list response.
type wifiNetwork struct {
	SSID string `json:"ssid"`
	PSK  string `json:"psk"`
}
