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

// WifiNetwork is one network entry returned by CallWifiList.
type WifiNetwork struct {
	SSID string `json:"ssid"`
	PSK  string `json:"psk"`
}

// callSocket is the shared low-level transport for all socket ops.
// It dials, encodes the request, decodes the response, and returns it.
func callSocket(ctx context.Context, socketPath string, req authRequest) (authResponse, error) {
	var resp authResponse
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		log.Printf("authz: op=%q dial failed: %v", req.Op, err)
		return resp, fmt.Errorf("auth socket unavailable: %w", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		log.Printf("authz: op=%q encode failed: %v", req.Op, err)
		return resp, fmt.Errorf("encode op request: %w", err)
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		log.Printf("authz: op=%q decode response failed: %v", req.Op, err)
		return resp, fmt.Errorf("decode op response: %w", err)
	}
	log.Printf("authz: op=%q result ok=%v error=%q", req.Op, resp.Ok, resp.Error)
	return resp, nil
}

// CallOp sends a simple privileged operation (e.g. "wan_up", "wan_down").
// The caller is responsible for verifying admin role before calling.
func CallOp(ctx context.Context, socketPath, op string) (bool, string, error) {
	log.Printf("authz: sending op=%q to helper at %s", op, socketPath)
	resp, err := callSocket(ctx, socketPath, authRequest{Op: op})
	if err != nil {
		return false, "", err
	}
	return resp.Ok, resp.Error, nil
}

// CallWifiList retrieves the current list of configured WiFi networks.
func CallWifiList(ctx context.Context, socketPath string) ([]WifiNetwork, error) {
	log.Printf("authz: sending op=%q to helper at %s", "wifi_list", socketPath)
	resp, err := callSocket(ctx, socketPath, authRequest{Op: "wifi_list"})
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	out := make([]WifiNetwork, len(resp.Networks))
	for i, n := range resp.Networks {
		out[i] = WifiNetwork{SSID: n.SSID, PSK: n.PSK}
	}
	log.Printf("authz: wifi_list returned %d network(s)", len(out))
	return out, nil
}

// CallWifiAdd adds a new WiFi network.
func CallWifiAdd(ctx context.Context, socketPath, ssid, psk string) error {
	log.Printf("authz: sending op=%q ssid=%q to helper at %s", "wifi_add", ssid, socketPath)
	resp, err := callSocket(ctx, socketPath, authRequest{Op: "wifi_add", SSID: ssid, PSK: psk})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// CallWifiRemove removes a WiFi network by SSID.
func CallWifiRemove(ctx context.Context, socketPath, ssid string) error {
	log.Printf("authz: sending op=%q ssid=%q to helper at %s", "wifi_remove", ssid, socketPath)
	resp, err := callSocket(ctx, socketPath, authRequest{Op: "wifi_remove", SSID: ssid})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// CallWifiReload triggers wpa_cli reconfigure on wlan1.
func CallWifiReload(ctx context.Context, socketPath string) error {
	log.Printf("authz: sending op=%q to helper at %s", "wifi_reload", socketPath)
	resp, err := callSocket(ctx, socketPath, authRequest{Op: "wifi_reload"})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}
