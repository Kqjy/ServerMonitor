package wgtunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const DefaultMTU = 1420

type DeviceConfig struct {
	PrivateKey Key
	Address    netip.Addr
	ListenPort uint16
	MTU        int
	Logger     *slog.Logger
}

type Peer struct {
	PublicKey           Key
	AllowedIPs          []netip.Prefix
	Endpoint            string
	PersistentKeepalive int
}

type PeerStats struct {
	PublicKey     Key
	Endpoint      string
	LastHandshake time.Time
	RxBytes       int64
	TxBytes       int64
}

type Device struct {
	dev  *device.Device
	net  *netstack.Net
	addr netip.Addr
}

func Start(cfg DeviceConfig, peers []Peer) (*Device, error) {
	if cfg.PrivateKey.IsZero() {
		return nil, fmt.Errorf("wireguard device: private key is required")
	}
	if !cfg.Address.IsValid() {
		return nil, fmt.Errorf("wireguard device: tunnel address is required")
	}
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{cfg.Address}, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("wireguard netstack: %w", err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), deviceLogger(cfg.Logger))

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", cfg.PrivateKey.hex())
	if cfg.ListenPort > 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", cfg.ListenPort)
	}
	b.WriteString("replace_peers=true\n")
	for _, peer := range peers {
		section, err := peerIPC(peer)
		if err != nil {
			dev.Close()
			return nil, err
		}
		b.WriteString(section)
	}
	if err := dev.IpcSet(b.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard configure: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard up: %w", err)
	}
	return &Device{dev: dev, net: tnet, addr: cfg.Address}, nil
}

func peerIPC(peer Peer) (string, error) {
	if peer.PublicKey.IsZero() {
		return "", fmt.Errorf("wireguard peer: public key is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "public_key=%s\n", peer.PublicKey.hex())
	b.WriteString("replace_allowed_ips=true\n")
	for _, prefix := range peer.AllowedIPs {
		fmt.Fprintf(&b, "allowed_ip=%s\n", prefix)
	}
	if peer.Endpoint != "" {
		resolved, err := resolveEndpoint(peer.Endpoint)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "endpoint=%s\n", resolved)
	}
	if peer.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", peer.PersistentKeepalive)
	}
	return b.String(), nil
}

func resolveEndpoint(endpoint string) (string, error) {
	if addrPort, err := netip.ParseAddrPort(endpoint); err == nil {
		return addrPort.String(), nil
	}
	udpAddr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return "", fmt.Errorf("resolve wireguard endpoint %q: %w", endpoint, err)
	}
	addr, ok := netip.AddrFromSlice(udpAddr.IP)
	if !ok {
		return "", fmt.Errorf("resolve wireguard endpoint %q: no usable address", endpoint)
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(udpAddr.Port)).String(), nil
}

func (d *Device) SetPeer(peer Peer) error {
	section, err := peerIPC(peer)
	if err != nil {
		return err
	}
	if err := d.dev.IpcSet(section); err != nil {
		return fmt.Errorf("wireguard set peer: %w", err)
	}
	return nil
}

func (d *Device) RemovePeer(pub Key) error {
	if err := d.dev.IpcSet(fmt.Sprintf("public_key=%s\nremove=true\n", pub.hex())); err != nil {
		return fmt.Errorf("wireguard remove peer: %w", err)
	}
	return nil
}

func (d *Device) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.net.DialContext(ctx, network, address)
}

func (d *Device) Listen(port uint16) (net.Listener, error) {
	ln, err := d.net.ListenTCPAddrPort(netip.AddrPortFrom(d.addr, port))
	if err != nil {
		return nil, fmt.Errorf("wireguard listen %d: %w", port, err)
	}
	return ln, nil
}

func (d *Device) Address() netip.Addr { return d.addr }

func (d *Device) PeerStats() ([]PeerStats, error) {
	raw, err := d.dev.IpcGet()
	if err != nil {
		return nil, fmt.Errorf("wireguard status: %w", err)
	}
	var out []PeerStats
	var current *PeerStats
	var handshakeSec, handshakeNsec int64
	flush := func() {
		if current == nil {
			return
		}
		if handshakeSec > 0 || handshakeNsec > 0 {
			current.LastHandshake = time.Unix(handshakeSec, handshakeNsec).UTC()
		}
		out = append(out, *current)
		current = nil
		handshakeSec, handshakeNsec = 0, 0
	}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "public_key":
			flush()
			parsed, err := parseHexKey(value)
			if err != nil {
				return nil, err
			}
			current = &PeerStats{PublicKey: parsed}
		case "endpoint":
			if current != nil {
				current.Endpoint = value
			}
		case "last_handshake_time_sec":
			handshakeSec, _ = strconv.ParseInt(value, 10, 64)
		case "last_handshake_time_nsec":
			handshakeNsec, _ = strconv.ParseInt(value, 10, 64)
		case "rx_bytes":
			if current != nil {
				current.RxBytes, _ = strconv.ParseInt(value, 10, 64)
			}
		case "tx_bytes":
			if current != nil {
				current.TxBytes, _ = strconv.ParseInt(value, 10, 64)
			}
		}
	}
	flush()
	return out, nil
}

func (d *Device) Close() {
	d.dev.Close()
}

func deviceLogger(logger *slog.Logger) *device.Logger {
	if logger == nil {
		return device.NewLogger(device.LogLevelSilent, "")
	}
	return &device.Logger{
		Verbosef: func(format string, args ...any) {},
		Errorf: func(format string, args ...any) {
			logger.Warn("wireguard", "msg", fmt.Sprintf(format, args...))
		},
	}
}
