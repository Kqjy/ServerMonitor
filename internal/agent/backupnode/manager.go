package backupnode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/restserver"
	"servermonitor/pkg/wgtunnel"
	"servermonitor/pkg/wire"
)

const (
	reconcileInterval   = time.Minute
	usageReportInterval = 5 * time.Minute
)

type Manager struct {
	serverURL string
	token     string
	dataDir   string
	logger    *slog.Logger
	client    *http.Client

	mu      sync.Mutex
	runtime *nodeRuntime
}

type nodeRuntime struct {
	device   *wgtunnel.Device
	srv      *http.Server
	registry *localRegistry
	spec     runtimeSpec
	peers    map[wgtunnel.Key]netip.Addr
}

type runtimeSpec struct {
	udpPort  int
	tunnelIP string
	storeDir string
	maxBlob  int64
	restPort int
}

func New(serverURL, token string, insecureSkip bool, dataDir string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{
		serverURL: strings.TrimRight(serverURL, "/"),
		token:     token,
		dataDir:   dataDir,
		logger:    logger,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkip},
			},
		},
	}
}

func (m *Manager) Run(ctx context.Context) {
	reconcile := time.NewTicker(reconcileInterval)
	usage := time.NewTicker(usageReportInterval)
	defer reconcile.Stop()
	defer usage.Stop()
	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			m.stop()
			return
		case <-reconcile.C:
			m.reconcile(ctx)
		case <-usage.C:
			m.reportUsage(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	cfg, err := m.fetchConfig(ctx)
	if err != nil {
		m.logger.Warn("backup node config fetch failed", "err", err)
		return
	}
	if !cfg.Enabled {
		if m.stop() {
			m.logger.Info("backup node role disabled; storage endpoint stopped")
		}
		return
	}

	key, err := m.ensureKey()
	if err != nil {
		m.logger.Error("backup node key", "err", err)
		return
	}
	enrolled, err := m.enroll(ctx, key.Public())
	if err != nil {
		m.logger.Warn("backup node enroll failed", "err", err)
		return
	}

	storeDir := strings.TrimSpace(cfg.StoreDir)
	if storeDir == "" {
		storeDir = filepath.Join(m.dataDir, "backup-store")
	}
	spec := runtimeSpec{
		udpPort:  cfg.UDPPort,
		tunnelIP: enrolled.TunnelIP,
		storeDir: storeDir,
		maxBlob:  cfg.MaxBlobBytes,
		restPort: cfg.RestPort,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.runtime != nil && m.runtime.spec == spec {
		added := m.runtime.registry.replaceTargets(cfg.Targets)
		if len(added) > 0 {
			store, storeErr := restserver.NewDiskStore(spec.storeDir)
			if storeErr != nil {
				m.logger.Error("backup node usage measurement failed", "err", storeErr)
				return
			}
			if measureErr := measureTargetUsage(ctx, store, m.runtime.registry, added); measureErr != nil {
				m.logger.Error("backup node usage measurement failed", "err", measureErr)
				return
			}
		}
		m.reconcilePeersLocked(cfg.Peers)
		return
	}

	m.stopLocked()
	rt, err := m.startRuntime(ctx, key, spec, cfg)
	if err != nil {
		m.logger.Error("backup node start failed", "err", err)
		return
	}
	m.runtime = rt
	m.logger.Info("backup node storage endpoint running",
		"udp_port", spec.udpPort,
		"tunnel_ip", spec.tunnelIP,
		"store_dir", spec.storeDir,
		"targets", len(cfg.Targets),
		"peers", len(cfg.Peers))
}

func (m *Manager) startRuntime(ctx context.Context, key wgtunnel.Key, spec runtimeSpec, cfg wire.BackupNodeConfig) (*nodeRuntime, error) {
	addr, err := netip.ParseAddr(spec.tunnelIP)
	if err != nil {
		return nil, fmt.Errorf("tunnel ip: %w", err)
	}
	peers, peerMap, err := buildPeers(cfg.Peers)
	if err != nil {
		return nil, err
	}
	device, err := wgtunnel.Start(wgtunnel.DeviceConfig{
		PrivateKey: key,
		Address:    addr,
		ListenPort: uint16(spec.udpPort),
		Logger:     m.logger,
	}, peers)
	if err != nil {
		return nil, err
	}
	store, err := restserver.NewDiskStore(spec.storeDir)
	if err != nil {
		device.Close()
		return nil, err
	}
	registry := newLocalRegistry(cfg.Targets)
	measureCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := measureTargetUsage(measureCtx, store, registry, cfg.Targets); err != nil {
		device.Close()
		return nil, err
	}
	restPort := spec.restPort
	if restPort <= 0 {
		restPort = restserver.TunnelRestPort
	}
	ln, err := device.Listen(uint16(restPort))
	if err != nil {
		device.Close()
		return nil, err
	}
	srv := &http.Server{
		Handler:           restserver.New(store, registry, spec.maxBlob, m.logger).Routes(),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			m.logger.Warn("backup node http server", "err", serveErr)
		}
	}()
	return &nodeRuntime{device: device, srv: srv, registry: registry, spec: spec, peers: peerMap}, nil
}

func buildPeers(infos []wire.NodePeerInfo) ([]wgtunnel.Peer, map[wgtunnel.Key]netip.Addr, error) {
	peers := make([]wgtunnel.Peer, 0, len(infos))
	peerMap := make(map[wgtunnel.Key]netip.Addr, len(infos))
	for _, info := range infos {
		key, err := wgtunnel.ParseKey(info.PublicKey)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(info.TunnelIP)
		if err != nil {
			continue
		}
		peers = append(peers, wgtunnel.Peer{
			PublicKey:  key,
			AllowedIPs: []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())},
		})
		peerMap[key] = addr
	}
	return peers, peerMap, nil
}

func (m *Manager) reconcilePeersLocked(infos []wire.NodePeerInfo) {
	rt := m.runtime
	if rt == nil {
		return
	}
	_, desired, _ := buildPeers(infos)
	for key, addr := range desired {
		if existing, ok := rt.peers[key]; !ok || existing != addr {
			if err := rt.device.SetPeer(wgtunnel.Peer{
				PublicKey:  key,
				AllowedIPs: []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())},
			}); err != nil {
				m.logger.Warn("backup node peer update failed", "err", err)
				continue
			}
			rt.peers[key] = addr
		}
	}
	for key := range rt.peers {
		if _, ok := desired[key]; !ok {
			if err := rt.device.RemovePeer(key); err != nil {
				m.logger.Warn("backup node peer removal failed", "err", err)
				continue
			}
			delete(rt.peers, key)
		}
	}
}

func measureTargetUsage(ctx context.Context, store restserver.Store, registry *localRegistry, targets []wire.NodeTargetInfo) error {
	for _, t := range targets {
		used, err := store.RepoUsage(ctx, t.Name)
		if err != nil {
			return fmt.Errorf("measure repository %q: %w", t.Name, err)
		}
		registry.setUsed(t.Name, used)
	}
	return nil
}

func (m *Manager) stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

func (m *Manager) stopLocked() bool {
	if m.runtime == nil {
		return false
	}
	_ = m.runtime.srv.Close()
	m.runtime.device.Close()
	m.runtime = nil
	return true
}

func (m *Manager) keyPath() string {
	rootOwned := "/etc/servermonitor-backup/tunnel.key"
	if runtime.GOOS == "windows" {
		if pd := strings.TrimSpace(os.Getenv("ProgramData")); pd != "" {
			rootOwned = filepath.Join(pd, "ServerMonitor", "Backup", "tunnel.key")
		} else {
			rootOwned = `C:\ProgramData\ServerMonitor\Backup\tunnel.key`
		}
	}
	if _, err := os.Stat(rootOwned); err == nil {
		return rootOwned
	}
	return filepath.Join(m.dataDir, "tunnel.key")
}

func (m *Manager) ensureKey() (wgtunnel.Key, error) {
	path := m.keyPath()
	data, err := os.ReadFile(path)
	if err == nil {
		return wgtunnel.ParseKey(strings.TrimSpace(string(data)))
	}
	if !os.IsNotExist(err) {
		return wgtunnel.Key{}, err
	}
	key, err := wgtunnel.GenerateKey()
	if err != nil {
		return wgtunnel.Key{}, err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return wgtunnel.Key{}, mkErr
	}
	if writeErr := os.WriteFile(path, []byte(key.String()+"\n"), 0o600); writeErr != nil {
		return wgtunnel.Key{}, writeErr
	}
	return key, nil
}

func (m *Manager) fetchConfig(ctx context.Context) (wire.BackupNodeConfig, error) {
	var cfg wire.BackupNodeConfig
	err := m.doJSON(ctx, http.MethodGet, "/api/v1/agent/backup-node", nil, &cfg)
	return cfg, err
}

func (m *Manager) enroll(ctx context.Context, pub wgtunnel.Key) (wire.TunnelEnrollResponse, error) {
	var resp wire.TunnelEnrollResponse
	err := m.doJSON(ctx, http.MethodPost, "/api/v1/agent/tunnel", wire.TunnelEnrollRequest{PublicKey: pub.String()}, &resp)
	return resp, err
}

func (m *Manager) reportUsage(ctx context.Context) {
	m.mu.Lock()
	rt := m.runtime
	var entries []wire.NodeUsageEntry
	if rt != nil {
		entries = rt.registry.usageSnapshot()
	}
	m.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	if err := m.doJSON(ctx, http.MethodPost, "/api/v1/agent/backup-node/usage", wire.BackupNodeUsage{Targets: entries}, nil); err != nil {
		m.logger.Debug("backup node usage report failed", "err", err)
	}
}

func (m *Manager) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.serverURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Agent-Token", m.token)
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type localTarget struct {
	id      int64
	hash    [32]byte
	quota   int64
	used    int64
	revoked bool
	ready   bool
	dirty   bool
}

type localRegistry struct {
	mu     sync.Mutex
	byName map[string]*localTarget
	byID   map[int64]string
	nextID int64
}

func newLocalRegistry(targets []wire.NodeTargetInfo) *localRegistry {
	r := &localRegistry{byName: map[string]*localTarget{}, byID: map[int64]string{}, nextID: 1}
	r.replaceTargets(targets)
	return r
}

func (r *localRegistry) replaceTargets(targets []wire.NodeTargetInfo) []wire.NodeTargetInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	added := []wire.NodeTargetInfo{}
	for _, t := range targets {
		seen[t.Name] = true
		raw, err := hex.DecodeString(t.SecretHash)
		if err != nil || len(raw) != 32 {
			continue
		}
		var hash [32]byte
		copy(hash[:], raw)
		if existing, ok := r.byName[t.Name]; ok {
			existing.hash = hash
			existing.quota = t.QuotaBytes
			existing.revoked = t.Revoked
			continue
		}
		id := r.nextID
		r.nextID++
		r.byName[t.Name] = &localTarget{id: id, hash: hash, quota: t.QuotaBytes, revoked: t.Revoked}
		r.byID[id] = t.Name
		added = append(added, t)
	}
	for name, t := range r.byName {
		if !seen[name] {
			delete(r.byID, t.id)
			delete(r.byName, name)
		}
	}
	return added
}

func (r *localRegistry) setUsed(name string, used int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.byName[name]; ok {
		t.used = used
		t.ready = true
		t.dirty = true
	}
}

func (r *localRegistry) usageSnapshot() []wire.NodeUsageEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []wire.NodeUsageEntry{}
	for name, t := range r.byName {
		if !t.dirty {
			continue
		}
		out = append(out, wire.NodeUsageEntry{Name: name, UsedBytes: t.used})
		t.dirty = false
	}
	return out
}

func (r *localRegistry) ResolveTarget(ctx context.Context, repo, secret string) (int64, int64, int64, bool, error) {
	candidate := sha256.Sum256([]byte(secret))
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byName[repo]
	if !ok {
		return 0, 0, 0, false, nil
	}
	if subtle.ConstantTimeCompare(t.hash[:], candidate[:]) != 1 {
		return 0, 0, 0, false, nil
	}
	if t.revoked || !t.ready {
		return 0, 0, 0, false, nil
	}
	return t.id, t.quota, t.used, true, nil
}

func (r *localRegistry) ReserveUsage(ctx context.Context, id int64, delta int64) (bool, error) {
	if delta < 0 {
		return false, errors.New("backup usage reservation must be non-negative")
	}
	if delta == 0 {
		return true, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.byID[id]
	if !ok {
		return false, nil
	}
	t := r.byName[name]
	if t.revoked || !t.ready || t.quota > 0 && (delta > t.quota || t.used > t.quota-delta) {
		return false, nil
	}
	t.used += delta
	t.dirty = true
	return true, nil
}

func (r *localRegistry) ReleaseUsage(ctx context.Context, id int64, delta int64) error {
	if delta <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.byID[id]
	if !ok {
		return nil
	}
	t := r.byName[name]
	t.used -= delta
	if t.used < 0 {
		t.used = 0
	}
	t.dirty = true
	return nil
}

var _ restserver.Registry = (*localRegistry)(nil)
