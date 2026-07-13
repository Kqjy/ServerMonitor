package restserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"servermonitor/pkg/wgtunnel"
)

const (
	TunnelRestPort   = 8443
	DefaultTunnelMTU = 1280
)

type TunnelConfig struct {
	PrivateKey wgtunnel.Key
	Subnet     netip.Prefix
	ListenPort uint16
	MTU        int
	Logger     *slog.Logger
}

type Tunnel struct {
	device     *wgtunnel.Device
	publicKey  wgtunnel.Key
	serverIP   netip.Addr
	subnet     netip.Prefix
	listenPort uint16
	httpServer *http.Server
}

func ServerTunnelIP(subnet netip.Prefix) netip.Addr {
	return subnet.Masked().Addr().Next()
}

func StartTunnel(cfg TunnelConfig, peers []wgtunnel.Peer, handler http.Handler) (*Tunnel, error) {
	serverIP := ServerTunnelIP(cfg.Subnet)
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = DefaultTunnelMTU
	}
	device, err := wgtunnel.Start(wgtunnel.DeviceConfig{
		PrivateKey: cfg.PrivateKey,
		Address:    serverIP,
		ListenPort: cfg.ListenPort,
		MTU:        mtu,
		Logger:     cfg.Logger,
	}, peers)
	if err != nil {
		return nil, err
	}
	var srv *http.Server
	if handler != nil {
		ln, listenErr := device.Listen(TunnelRestPort)
		if listenErr != nil {
			device.Close()
			return nil, listenErr
		}
		srv = &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 30 * time.Second,
		}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && cfg.Logger != nil {
				cfg.Logger.Warn("backup tunnel http server", "err", err)
			}
		}()
	}
	return &Tunnel{
		device:     device,
		publicKey:  cfg.PrivateKey.Public(),
		serverIP:   serverIP,
		subnet:     cfg.Subnet.Masked(),
		listenPort: cfg.ListenPort,
		httpServer: srv,
	}, nil
}

func PeerFromParts(publicKey []byte, tunnelIP netip.Addr) (wgtunnel.Peer, error) {
	key, err := wgtunnel.KeyFromBytes(publicKey)
	if err != nil {
		return wgtunnel.Peer{}, err
	}
	if !tunnelIP.IsValid() {
		return wgtunnel.Peer{}, fmt.Errorf("tunnel peer: invalid tunnel ip")
	}
	return wgtunnel.Peer{
		PublicKey:  key,
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(tunnelIP, tunnelIP.BitLen())},
	}, nil
}

func (t *Tunnel) SetPeer(publicKey []byte, tunnelIP netip.Addr) error {
	peer, err := PeerFromParts(publicKey, tunnelIP)
	if err != nil {
		return err
	}
	return t.device.SetPeer(peer)
}

func (t *Tunnel) RemovePeer(publicKey []byte) error {
	key, err := wgtunnel.KeyFromBytes(publicKey)
	if err != nil {
		return err
	}
	return t.device.RemovePeer(key)
}

func (t *Tunnel) PeerStats() ([]wgtunnel.PeerStats, error) {
	return t.device.PeerStats()
}

func (t *Tunnel) PublicKey() wgtunnel.Key { return t.publicKey }

func (t *Tunnel) ServerIP() netip.Addr { return t.serverIP }

func (t *Tunnel) Subnet() netip.Prefix { return t.subnet }

func (t *Tunnel) ListenPort() uint16 { return t.listenPort }

func (t *Tunnel) Close() {
	if t.httpServer != nil {
		_ = t.httpServer.Close()
	}
	t.device.Close()
}
