package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// CallOp sends a privileged operation request (e.g. "wan_up", "wan_down")
// to the pispot-authd Unix socket. The caller is responsible for verifying
// admin role before calling this function. Returns ok=true on success.
func CallOp(ctx context.Context, socketPath, op string) (bool, string, error) {
	log.Printf("authz: sending op=%q to helper at %s", op, socketPath)
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		log.Printf("authz: op=%q dial failed: %v", op, err)
		return false, "", fmt.Errorf("auth socket unavailable: %w", err)
	}
	defer conn.Close()

	req := authRequest{Op: op}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		log.Printf("authz: op=%q encode failed: %v", op, err)
		return false, "", fmt.Errorf("encode op request: %w", err)
	}

	var resp authResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		log.Printf("authz: op=%q decode response failed: %v", op, err)
		return false, "", fmt.Errorf("decode op response: %w", err)
	}
	log.Printf("authz: op=%q result ok=%v error=%q", op, resp.Ok, resp.Error)
	return resp.Ok, resp.Error, nil
}
