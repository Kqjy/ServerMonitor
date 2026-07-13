package api

import (
	"net/http/httptest"
	"testing"
)

func TestTunnelEndpointForRequest(t *testing.T) {
	cases := []struct {
		name string
		info BackupTunnelInfo
		host string
		want string
	}{
		{"explicit with port", BackupTunnelInfo{Endpoint: "wg.example.com:52000", ListenPort: 51820}, "monitor.example.com", "wg.example.com:52000"},
		{"explicit host only", BackupTunnelInfo{Endpoint: "wg.example.com", ListenPort: 51820}, "monitor.example.com", "wg.example.com:51820"},
		{"derived from host header", BackupTunnelInfo{ListenPort: 51820}, "monitor.example.com", "monitor.example.com:51820"},
		{"derived strips port", BackupTunnelInfo{ListenPort: 51820}, "monitor.example.com:8443", "monitor.example.com:51820"},
		{"ipv6 host header", BackupTunnelInfo{ListenPort: 51820}, "[2001:db8::1]:8443", "[2001:db8::1]:51820"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/agent/tunnel", nil)
			r.Host = tc.host
			if got := tunnelEndpointForRequest(tc.info, r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
