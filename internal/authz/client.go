package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// dialTimeout is the maximum time to wait for the pispot-authd socket
// to accept a connection.
const dialTimeout = 2 * time.Second

// callHelper sends an auth request to the pispot-authd Unix socket and
// returns the response. Returns an error if the socket is unavailable
// or the response cannot be decoded.
func callHelper(ctx context.Context, socketPath, username, password string) (authResponse, error) {
	var resp authResponse

	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return resp, fmt.Errorf("auth socket unavailable: %w", err)
	}
	defer conn.Close()

	req := authRequest{
		Op:       "auth",
		Username: username,
		Password: password,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return resp, fmt.Errorf("encode auth request: %w", err)
	}

	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return resp, fmt.Errorf("decode auth response: %w", err)
	}
	return resp, nil
}
