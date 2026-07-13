package wgtunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestKeyRoundTrip(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseKey(k.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != k {
		t.Fatalf("round trip mismatch: %s != %s", parsed, k)
	}
	if k.Public().IsZero() {
		t.Fatal("public key is zero")
	}
	if k.Public() != parsed.Public() {
		t.Fatal("public derivation not deterministic")
	}
	if _, err := ParseKey("not-base64!!"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := ParseKey("aGVsbG8="); err == nil {
		t.Fatal("expected length error")
	}
}

func TestTunnelHTTPRoundTrip(t *testing.T) {
	serverKey, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	serverAddr := netip.MustParseAddr("10.83.0.1")
	clientAddr := netip.MustParseAddr("10.83.0.2")

	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(udp.LocalAddr().(*net.UDPAddr).Port)
	udp.Close()

	server, err := Start(DeviceConfig{
		PrivateKey: serverKey,
		Address:    serverAddr,
		ListenPort: port,
	}, []Peer{{
		PublicKey:  clientKey.Public(),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(clientAddr, 32)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	ln, err := server.Listen(8080)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "echo:%d", len(body))
	}))

	client, err := Start(DeviceConfig{
		PrivateKey: clientKey,
		Address:    clientAddr,
	}, []Peer{{
		PublicKey:           serverKey.Public(),
		AllowedIPs:          []netip.Prefix{netip.PrefixFrom(serverAddr, 32)},
		Endpoint:            fmt.Sprintf("127.0.0.1:%d", port),
		PersistentKeepalive: 25,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	httpClient := &http.Client{
		Transport: &http.Transport{DialContext: client.DialContext},
		Timeout:   10 * time.Second,
	}
	resp, err := httpClient.Post(fmt.Sprintf("http://%s:8080/blob", serverAddr), "application/octet-stream", io.LimitReader(neverEnding('x'), 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != fmt.Sprintf("echo:%d", 1<<20) {
		t.Fatalf("unexpected response %q", body)
	}

	stats, err := server.PeerStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(stats))
	}
	if stats[0].PublicKey != clientKey.Public() {
		t.Fatal("peer stats public key mismatch")
	}
	if stats[0].LastHandshake.IsZero() {
		t.Fatal("expected a completed handshake")
	}
	if stats[0].RxBytes == 0 {
		t.Fatal("expected nonzero rx bytes")
	}

	if err := server.RemovePeer(clientKey.Public()); err != nil {
		t.Fatal(err)
	}
	stats, err = server.PeerStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Fatalf("expected 0 peers after removal, got %d", len(stats))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	shortClient := &http.Client{Transport: &http.Transport{DialContext: client.DialContext}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:8080/", serverAddr), nil)
	if _, err := shortClient.Do(req); err == nil {
		t.Fatal("expected request to fail after peer removal")
	}
}

type neverEnding byte

func (b neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
