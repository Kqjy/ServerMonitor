package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"servermonitor/internal/server/storage"
	"servermonitor/pkg/restserver"
	"servermonitor/pkg/wgtunnel"
	"servermonitor/pkg/wire"
)

type BackupTunnelInfo struct {
	Enabled       bool
	ListenPort    int
	Endpoint      string
	Subnet        string
	PublicHTTP    bool
	ServerStorage bool
}

type tunnelPeerView struct {
	HostID        int64      `json:"host_id"`
	Hostname      string     `json:"hostname"`
	TunnelIP      string     `json:"tunnel_ip"`
	EnrolledAt    time.Time  `json:"enrolled_at"`
	LastHandshake *time.Time `json:"last_handshake,omitempty"`
	RxBytes       int64      `json:"rx_bytes"`
	TxBytes       int64      `json:"tx_bytes"`
	Connected     bool       `json:"connected"`
}

type backupTunnelResponse struct {
	Enabled         bool             `json:"enabled"`
	PublicHTTP      bool             `json:"public_http"`
	ListenPort      int              `json:"listen_port,omitempty"`
	Endpoint        string           `json:"endpoint,omitempty"`
	Subnet          string           `json:"subnet,omitempty"`
	ServerPublicKey string           `json:"server_public_key,omitempty"`
	ServerTunnelIP  string           `json:"server_tunnel_ip,omitempty"`
	RestPort        int              `json:"rest_port,omitempty"`
	Peers           []tunnelPeerView `json:"peers"`
}

const tunnelHandshakeConnectedWindow = 3 * time.Minute

func tunnelEndpointForRequest(info BackupTunnelInfo, r *http.Request) string {
	endpoint := strings.TrimSpace(info.Endpoint)
	if endpoint != "" {
		if _, _, err := net.SplitHostPort(endpoint); err == nil {
			return endpoint
		}
		return net.JoinHostPort(endpoint, strconv.Itoa(info.ListenPort))
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(info.ListenPort))
}

func tunnelEnrollHandler(tunnel *restserver.Tunnel, peers *storage.BackupTunnelStore, info BackupTunnelInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tunnel == nil {
			writeError(w, http.StatusServiceUnavailable, "backup tunnel is not enabled on this server")
			return
		}
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		var req wire.TunnelEnrollRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		pub, err := wgtunnel.ParseKey(strings.TrimSpace(req.PublicKey))
		if err != nil || pub.IsZero() {
			writeError(w, http.StatusBadRequest, "public_key must be a base64 wireguard public key")
			return
		}
		ip, err := peers.EnrollPeer(r.Context(), hostID, pub[:], tunnel.Subnet(), tunnel.ServerIP())
		if err != nil {
			if errors.Is(err, storage.ErrTunnelKeyTaken) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "enroll failed")
			return
		}
		if err := tunnel.SetPeer(pub[:], ip); err != nil {
			writeError(w, http.StatusInternalServerError, "peer activation failed")
			return
		}
		endpoint := tunnelEndpointForRequest(info, r)
		if endpoint == "" {
			writeError(w, http.StatusInternalServerError, "cannot determine tunnel endpoint; set BACKUP_WG_ENDPOINT")
			return
		}
		writeJSON(w, http.StatusOK, wire.TunnelEnrollResponse{
			ServerPublicKey: tunnel.PublicKey().String(),
			Endpoint:        endpoint,
			TunnelIP:        ip.String(),
			ServerTunnelIP:  tunnel.ServerIP().String(),
			RestPort:        restserver.TunnelRestPort,
			ServerStorage:   info.ServerStorage,
		})
	}
}

func backupTunnelStatusHandler(tunnel *restserver.Tunnel, peers *storage.BackupTunnelStore, info BackupTunnelInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := backupTunnelResponse{Enabled: tunnel != nil, PublicHTTP: info.PublicHTTP, Peers: []tunnelPeerView{}}
		if tunnel == nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp.ListenPort = info.ListenPort
		resp.Endpoint = tunnelEndpointForRequest(info, r)
		resp.Subnet = info.Subnet
		resp.ServerTunnelIP = tunnel.ServerIP().String()
		resp.RestPort = restserver.TunnelRestPort
		resp.ServerPublicKey = tunnel.PublicKey().String()
		rows, err := peers.ListPeers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		statsByKey := map[wgtunnel.Key]wgtunnel.PeerStats{}
		if stats, err := tunnel.PeerStats(); err == nil {
			for _, s := range stats {
				statsByKey[s.PublicKey] = s
			}
		}
		now := time.Now()
		for _, p := range rows {
			view := tunnelPeerView{
				HostID:     p.HostID,
				Hostname:   p.Hostname,
				TunnelIP:   p.TunnelIP.String(),
				EnrolledAt: p.EnrolledAt,
			}
			if key, err := wgtunnel.KeyFromBytes(p.PublicKey); err == nil {
				if s, ok := statsByKey[key]; ok {
					view.RxBytes = s.RxBytes
					view.TxBytes = s.TxBytes
					if !s.LastHandshake.IsZero() {
						hs := s.LastHandshake
						view.LastHandshake = &hs
						view.Connected = now.Sub(hs) < tunnelHandshakeConnectedWindow
					}
				}
			}
			resp.Peers = append(resp.Peers, view)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func agentTunnelNodesHandler(nodes *storage.BackupNodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := nodes.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "node listing failed")
			return
		}
		resp := wire.TunnelNodesResponse{Nodes: []wire.TunnelNodeInfo{}}
		for _, node := range rows {
			info := wire.TunnelNodeInfo{
				HostID:   node.HostID,
				Hostname: node.Hostname,
				Endpoint: nodeEndpointWithPort(node.Endpoint, node.UDPPort),
				RestPort: restserver.TunnelRestPort,
			}
			if node.TunnelIP != nil {
				info.TunnelIP = node.TunnelIP.String()
			}
			if key, keyErr := wgtunnel.KeyFromBytes(node.PublicKey); keyErr == nil {
				info.PublicKey = key.String()
			}
			resp.Nodes = append(resp.Nodes, info)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func nodeEndpointWithPort(endpoint string, udpPort int) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(endpoint); err == nil {
		return endpoint
	}
	return net.JoinHostPort(endpoint, strconv.Itoa(udpPort))
}

func agentBackupNodeConfigHandler(nodes *storage.BackupNodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		cfg, err := nodes.ConfigForHost(r.Context(), hostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "node config failed")
			return
		}
		resp := wire.BackupNodeConfig{Enabled: cfg.Enabled}
		if cfg.Enabled {
			resp.UDPPort = cfg.UDPPort
			resp.StoreDir = cfg.StoreDir
			resp.MaxBlobBytes = cfg.MaxBlobBytes
			resp.RestPort = restserver.TunnelRestPort
			if cfg.TunnelIP != nil {
				resp.TunnelIP = cfg.TunnelIP.String()
			}
			for _, t := range cfg.Targets {
				resp.Targets = append(resp.Targets, wire.NodeTargetInfo{
					Name:       t.Name,
					SecretHash: hex.EncodeToString(t.SecretHash),
					QuotaBytes: t.QuotaBytes,
					Revoked:    t.Revoked,
				})
			}
			for _, p := range cfg.Peers {
				if key, keyErr := wgtunnel.KeyFromBytes(p.PublicKey); keyErr == nil {
					resp.Peers = append(resp.Peers, wire.NodePeerInfo{
						PublicKey: key.String(),
						TunnelIP:  p.TunnelIP.String(),
					})
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func agentBackupNodeUsageHandler(nodes *storage.BackupNodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		var req wire.BackupNodeUsage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if _, err := nodes.Get(r.Context(), hostID); err != nil {
			writeError(w, http.StatusForbidden, "host is not a backup node")
			return
		}
		for _, t := range req.Targets {
			if !validBackupTargetName(t.Name) {
				continue
			}
			if err := nodes.SetTargetUsage(r.Context(), hostID, t.Name, t.UsedBytes); err != nil {
				writeError(w, http.StatusInternalServerError, "usage update failed")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

type backupNodeView struct {
	HostID      int64     `json:"host_id"`
	Hostname    string    `json:"hostname"`
	UDPPort     int       `json:"udp_port"`
	Endpoint    string    `json:"endpoint"`
	StoreDir    string    `json:"store_dir,omitempty"`
	TunnelIP    string    `json:"tunnel_ip,omitempty"`
	Enrolled    bool      `json:"enrolled"`
	TargetCount int       `json:"target_count"`
	UsedBytes   int64     `json:"used_bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

func listBackupNodesHandler(nodes *storage.BackupNodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := nodes.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := []backupNodeView{}
		for _, node := range rows {
			view := backupNodeView{
				HostID:      node.HostID,
				Hostname:    node.Hostname,
				UDPPort:     node.UDPPort,
				Endpoint:    node.Endpoint,
				StoreDir:    node.StoreDir,
				Enrolled:    node.TunnelIP != nil,
				TargetCount: node.TargetCount,
				UsedBytes:   node.UsedBytes,
				CreatedAt:   node.CreatedAt,
			}
			if node.TunnelIP != nil {
				view.TunnelIP = node.TunnelIP.String()
			}
			out = append(out, view)
		}
		writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
	}
}

type promoteNodeRequest struct {
	HostID       int64  `json:"host_id"`
	UDPPort      int    `json:"udp_port,omitempty"`
	Endpoint     string `json:"endpoint"`
	StoreDir     string `json:"store_dir,omitempty"`
	MaxBlobBytes int64  `json:"max_blob_bytes,omitempty"`
}

func promoteBackupNodeHandler(nodes *storage.BackupNodes, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req promoteNodeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.HostID <= 0 {
			writeError(w, http.StatusBadRequest, "host_id is required")
			return
		}
		if _, err := hosts.Get(r.Context(), req.HostID); err != nil {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		endpoint := strings.TrimSpace(req.Endpoint)
		if endpoint == "" {
			writeError(w, http.StatusBadRequest, "endpoint is required (host or host:port agents can reach this node's UDP tunnel at)")
			return
		}
		host := endpoint
		if h, _, err := net.SplitHostPort(endpoint); err == nil {
			host = h
		}
		if strings.TrimSpace(host) == "" {
			writeError(w, http.StatusBadRequest, "endpoint must be host or host:port")
			return
		}
		port := req.UDPPort
		if port == 0 {
			port = 51821
		}
		if port < 1 || port > 65535 {
			writeError(w, http.StatusBadRequest, "udp_port must be between 1 and 65535")
			return
		}
		maxBlob := req.MaxBlobBytes
		if maxBlob <= 0 {
			maxBlob = 1 << 30
		}
		if err := nodes.Promote(r.Context(), req.HostID, port, endpoint, strings.TrimSpace(req.StoreDir), maxBlob); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func demoteBackupNodeHandler(nodes *storage.BackupNodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, err := strconv.ParseInt(chi.URLParam(r, "hostID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid host id")
			return
		}
		if err := nodes.Demote(r.Context(), hostID); err != nil {
			switch {
			case errors.Is(err, storage.ErrNodeHasTargets):
				writeError(w, http.StatusConflict, err.Error())
			case errors.Is(err, storage.ErrNotFound):
				writeError(w, http.StatusNotFound, "node not found")
			default:
				writeError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func revokeTunnelPeerHandler(tunnel *restserver.Tunnel, peers *storage.BackupTunnelStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tunnel == nil {
			writeError(w, http.StatusServiceUnavailable, "backup tunnel is not enabled on this server")
			return
		}
		hostID, err := strconv.ParseInt(chi.URLParam(r, "hostID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid host id")
			return
		}
		peer, err := peers.GetPeer(r.Context(), hostID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "peer not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := peers.DeletePeer(r.Context(), hostID); err != nil && !errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := tunnel.RemovePeer(peer.PublicKey); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
