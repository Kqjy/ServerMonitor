package backup

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/wgtunnel"
)

const tunnelKeepaliveSeconds = 25

type tunnelDestination struct {
	listener  net.Listener
	proxyAddr string
	target    string
}

type TunnelSession struct {
	device       *wgtunnel.Device
	logger       *slog.Logger
	destinations map[string]*tunnelDestination

	mu     sync.Mutex
	closed bool
}

func PrepareTunnel(cfg Config, opts Options) (Config, *TunnelSession) {
	if !cfg.HasTunnelRepos() {
		return cfg, nil
	}
	needed := map[string]bool{}
	for _, repo := range cfg.Repos {
		if repo.UsesTunnel() {
			needed[repo.TunnelNode] = true
		}
	}
	session, err := startTunnelSession(cfg.Tunnel, needed, "", opts.logger())
	for i := range cfg.Repos {
		if !cfg.Repos[i].UsesTunnel() {
			continue
		}
		if err != nil {
			cfg.Repos[i].tunnelErr = err
			continue
		}
		url, urlErr := session.repoURL(cfg.Repos[i].TunnelNode, cfg.Repos[i].TunnelName)
		if urlErr != nil {
			cfg.Repos[i].tunnelErr = urlErr
			continue
		}
		cfg.Repos[i].URL = url
	}
	if err != nil {
		opts.logger().Error("backup tunnel unavailable; tunnel repos will fail this run", "err", err)
		return cfg, nil
	}
	return cfg, session
}

func startTunnelSession(settings *TunnelSettings, needed map[string]bool, listenAddr string, logger *slog.Logger) (*TunnelSession, error) {
	if settings == nil {
		return nil, fmt.Errorf("backup tunnel: [tunnel] section missing")
	}
	if err := settings.validate(needed[""]); err != nil {
		return nil, fmt.Errorf("backup tunnel: %w", err)
	}
	keyText, err := os.ReadFile(settings.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("backup tunnel: read private key: %w", err)
	}
	privateKey, err := wgtunnel.ParseKey(strings.TrimSpace(string(keyText)))
	if err != nil {
		return nil, fmt.Errorf("backup tunnel: private key: %w", err)
	}
	localIP, err := netip.ParseAddr(settings.LocalIP)
	if err != nil {
		return nil, fmt.Errorf("backup tunnel: local_ip: %w", err)
	}

	type destSpec struct {
		key      string
		peerKey  string
		endpoint string
		ip       string
		restPort int
	}
	specs := []destSpec{}
	if needed[""] {
		specs = append(specs, destSpec{
			key:      "",
			peerKey:  settings.ServerPublicKey,
			endpoint: settings.Endpoint,
			ip:       settings.ServerIP,
			restPort: settings.RestPort,
		})
	}
	for host := range needed {
		if host == "" {
			continue
		}
		node := settings.Node(host)
		if node == nil {
			return nil, fmt.Errorf("backup tunnel: node %q has no [[tunnel.node]] entry", host)
		}
		specs = append(specs, destSpec{
			key:      host,
			peerKey:  node.PublicKey,
			endpoint: node.Endpoint,
			ip:       node.IP,
			restPort: node.RestPort,
		})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].key < specs[j].key })

	peers := make([]wgtunnel.Peer, 0, len(specs))
	for _, spec := range specs {
		peerKey, keyErr := wgtunnel.ParseKey(spec.peerKey)
		if keyErr != nil {
			return nil, fmt.Errorf("backup tunnel: peer key for %q: %w", destLabel(spec.key), keyErr)
		}
		peerIP, ipErr := netip.ParseAddr(spec.ip)
		if ipErr != nil {
			return nil, fmt.Errorf("backup tunnel: peer ip for %q: %w", destLabel(spec.key), ipErr)
		}
		peers = append(peers, wgtunnel.Peer{
			PublicKey:           peerKey,
			AllowedIPs:          []netip.Prefix{netip.PrefixFrom(peerIP, peerIP.BitLen())},
			Endpoint:            spec.endpoint,
			PersistentKeepalive: tunnelKeepaliveSeconds,
		})
	}

	device, err := wgtunnel.Start(wgtunnel.DeviceConfig{
		PrivateKey: privateKey,
		Address:    localIP,
		MTU:        settings.MTU,
		Logger:     logger,
	}, peers)
	if err != nil {
		return nil, fmt.Errorf("backup tunnel: %w", err)
	}

	session := &TunnelSession{
		device:       device,
		logger:       logger,
		destinations: map[string]*tunnelDestination{},
	}
	for _, spec := range specs {
		addr := "127.0.0.1:0"
		if listenAddr != "" {
			if len(specs) > 1 {
				session.Close()
				return nil, fmt.Errorf("backup tunnel: --listen supports a single destination, but %d are configured", len(specs))
			}
			addr = listenAddr
		}
		if !strings.HasPrefix(addr, "127.") && !strings.HasPrefix(addr, "[::1]") {
			session.Close()
			return nil, fmt.Errorf("backup tunnel: proxy listener must bind loopback, got %q", addr)
		}
		listener, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			session.Close()
			return nil, fmt.Errorf("backup tunnel: loopback listener: %w", listenErr)
		}
		peerIP := netip.MustParseAddr(spec.ip)
		dest := &tunnelDestination{
			listener:  listener,
			proxyAddr: listener.Addr().String(),
			target:    netip.AddrPortFrom(peerIP, uint16(spec.restPort)).String(),
		}
		session.destinations[spec.key] = dest
		go session.acceptLoop(dest)
	}
	return session, nil
}

func destLabel(key string) string {
	if key == "" {
		return "server"
	}
	return key
}

func (s *TunnelSession) acceptLoop(dest *tunnelDestination) {
	for {
		conn, err := dest.listener.Accept()
		if err != nil {
			return
		}
		go s.forward(conn, dest.target)
	}
}

func (s *TunnelSession) forward(local net.Conn, target string) {
	defer local.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	remote, err := s.device.DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		s.logger.Warn("backup tunnel dial failed", "target", target, "err", err)
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go proxyCopy(remote, local, done)
	go proxyCopy(local, remote, done)
	<-done
	<-done
}

func proxyCopy(dst io.Writer, src io.Reader, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
	done <- struct{}{}
}

func (s *TunnelSession) repoURL(node, tunnelName string) (string, error) {
	dest, ok := s.destinations[node]
	if !ok {
		return "", fmt.Errorf("backup tunnel: no active destination for %q", destLabel(node))
	}
	return fmt.Sprintf("rest:http://%s/%s", dest.proxyAddr, tunnelName), nil
}

func (s *TunnelSession) Handshaken(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		stats, err := s.device.PeerStats()
		if err == nil && len(stats) > 0 {
			ready := true
			for _, stat := range stats {
				if stat.LastHandshake.IsZero() {
					ready = false
					break
				}
			}
			if ready {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no wireguard handshake within %s (is every destination reachable over UDP and this host enrolled?)", timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *TunnelSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for _, dest := range s.destinations {
		_ = dest.listener.Close()
	}
	s.device.Close()
}

type ProxyEndpoint struct {
	Destination string
	RepoName    string
	RepoURL     string
}

type ProxyInfo struct {
	Session   *TunnelSession
	Release   func()
	Endpoints []ProxyEndpoint
}

func StartProxy(cfg Config, listenAddr string, opts Options) (ProxyInfo, error) {
	cfg = normalizedConfig(cfg)
	if cfg.Tunnel == nil {
		return ProxyInfo{}, fmt.Errorf("backup.toml has no [tunnel] section; run sm-agent backup tunnel-enroll first")
	}
	release, busy, err := acquireRunLock(cfg.StatusPath, opts.now())
	if err != nil {
		return ProxyInfo{}, err
	}
	if busy {
		return ProxyInfo{}, fmt.Errorf("another backup operation holds the tunnel lock; retry when it finishes")
	}
	needed := map[string]bool{}
	for _, repo := range cfg.Repos {
		if repo.UsesTunnel() {
			needed[repo.TunnelNode] = true
		}
	}
	if len(needed) == 0 {
		release()
		return ProxyInfo{}, fmt.Errorf("no tunnel repositories are configured")
	}
	if listenAddr == "127.0.0.1:0" {
		listenAddr = ""
	}
	session, err := startTunnelSession(cfg.Tunnel, needed, listenAddr, opts.logger())
	if err != nil {
		release()
		return ProxyInfo{}, err
	}
	endpoints := []ProxyEndpoint{}
	for _, repo := range cfg.Repos {
		if !repo.UsesTunnel() {
			continue
		}
		url, urlErr := session.repoURL(repo.TunnelNode, repo.TunnelName)
		if urlErr != nil {
			continue
		}
		endpoints = append(endpoints, ProxyEndpoint{
			Destination: destLabel(repo.TunnelNode),
			RepoName:    repo.Name,
			RepoURL:     url,
		})
	}
	return ProxyInfo{Session: session, Release: release, Endpoints: endpoints}, nil
}

func tunnelLockGuard(cfg Config, opts Options) (func(), error) {
	if !cfg.HasTunnelRepos() {
		return func() {}, nil
	}
	release, busy, err := acquireRunLock(cfg.StatusPath, opts.now())
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, fmt.Errorf("another backup operation holds the tunnel lock; retry when it finishes")
	}
	return release, nil
}

func defaultTunnelKeyPath(configPath string) string {
	dir := configDir(configPath)
	if runtime.GOOS == "windows" {
		return dir + `\tunnel.key`
	}
	return dir + "/tunnel.key"
}

func configDir(configPath string) string {
	idx := strings.LastIndexAny(configPath, `/\`)
	if idx <= 0 {
		return "."
	}
	return configPath[:idx]
}
