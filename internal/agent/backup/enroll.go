package backup

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"servermonitor/pkg/wgtunnel"
	"servermonitor/pkg/wire"
)

type EnrollOptions struct {
	ConfigPath   string
	ServerURL    string
	Token        string
	InsecureSkip bool
	KeyPath      string
	HTTPClient   *http.Client
}

type EnrollResult struct {
	TunnelIP        string
	ServerTunnelIP  string
	Endpoint        string
	ServerPublicKey string
	KeyPath         string
	KeyGenerated    bool
	Nodes           []TunnelNodeSettings
	Warning         string
}

func TunnelEnroll(ctx context.Context, opts EnrollOptions) (EnrollResult, error) {
	if strings.TrimSpace(opts.ConfigPath) == "" {
		opts.ConfigPath = DefaultConfigPath()
	}
	if strings.TrimSpace(opts.ServerURL) == "" || strings.TrimSpace(opts.Token) == "" {
		return EnrollResult{}, fmt.Errorf("server url and agent token are required (is agent.toml present?)")
	}
	keyPath := strings.TrimSpace(opts.KeyPath)
	if keyPath == "" {
		keyPath = defaultTunnelKeyPath(opts.ConfigPath)
	}

	needServer, nodeHosts, err := tunnelRepoDemand(opts.ConfigPath)
	if err != nil {
		return EnrollResult{}, err
	}

	privateKey, generated, err := loadOrCreateTunnelKey(keyPath)
	if err != nil {
		return EnrollResult{}, err
	}

	resp, err := postEnroll(ctx, opts, privateKey.Public())
	if err != nil {
		return EnrollResult{}, err
	}

	var nodes []TunnelNodeSettings
	if len(nodeHosts) > 0 {
		nodes, err = resolveNodes(ctx, opts, nodeHosts)
		if err != nil {
			return EnrollResult{}, err
		}
	}

	settings := TunnelSettings{
		PrivateKeyFile:  keyPath,
		ServerPublicKey: resp.ServerPublicKey,
		Endpoint:        resp.Endpoint,
		LocalIP:         resp.TunnelIP,
		ServerIP:        resp.ServerTunnelIP,
		RestPort:        resp.RestPort,
		Nodes:           nodes,
	}
	if err := settings.validate(needServer); err != nil {
		return EnrollResult{}, fmt.Errorf("server returned an incomplete enrollment: %w", err)
	}
	warning := ""
	if needServer && !resp.ServerStorage {
		warning = "this ServerMonitor server has no storage backend (BACKUP_DIR/BACKUP_S3_BUCKET); repos targeting the server will fail until one is configured"
	}
	if err := writeTunnelSettings(opts.ConfigPath, settings); err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{
		TunnelIP:        resp.TunnelIP,
		ServerTunnelIP:  resp.ServerTunnelIP,
		Endpoint:        resp.Endpoint,
		ServerPublicKey: resp.ServerPublicKey,
		KeyPath:         keyPath,
		KeyGenerated:    generated,
		Nodes:           nodes,
		Warning:         warning,
	}, nil
}

func tunnelRepoDemand(configPath string) (bool, []string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false, nil, fmt.Errorf("read backup config %s: %w", configPath, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return false, nil, fmt.Errorf("parse backup config %s: %w", configPath, err)
	}
	needServer := false
	seen := map[string]bool{}
	hosts := []string{}
	for _, repo := range raw.Repos {
		if strings.TrimSpace(repo.TunnelName) == "" {
			continue
		}
		node := strings.TrimSpace(repo.TunnelNode)
		if node == "" {
			needServer = true
			continue
		}
		if !seen[node] {
			seen[node] = true
			hosts = append(hosts, node)
		}
	}
	return needServer, hosts, nil
}

func resolveNodes(ctx context.Context, opts EnrollOptions, hosts []string) ([]TunnelNodeSettings, error) {
	var listing wire.TunnelNodesResponse
	if err := getJSON(ctx, opts, "/api/v1/agent/tunnel/nodes", &listing); err != nil {
		return nil, fmt.Errorf("node lookup failed: %w", err)
	}
	byName := map[string]wire.TunnelNodeInfo{}
	for _, node := range listing.Nodes {
		byName[node.Hostname] = node
	}
	out := make([]TunnelNodeSettings, 0, len(hosts))
	for _, host := range hosts {
		info, ok := byName[host]
		if !ok {
			return nil, fmt.Errorf("host %q is not a promoted backup node on the server", host)
		}
		if info.PublicKey == "" || info.TunnelIP == "" {
			return nil, fmt.Errorf("backup node %q has not come online yet (its agent enrolls within a minute of promotion); retry shortly", host)
		}
		if info.Endpoint == "" {
			return nil, fmt.Errorf("backup node %q has no endpoint configured on the server", host)
		}
		out = append(out, TunnelNodeSettings{
			Host:      host,
			PublicKey: info.PublicKey,
			Endpoint:  info.Endpoint,
			IP:        info.TunnelIP,
			RestPort:  info.RestPort,
		})
	}
	return out, nil
}

func loadOrCreateTunnelKey(path string) (wgtunnel.Key, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		key, parseErr := wgtunnel.ParseKey(strings.TrimSpace(string(data)))
		if parseErr != nil {
			return wgtunnel.Key{}, false, fmt.Errorf("existing tunnel key %s is invalid: %w", path, parseErr)
		}
		return key, false, nil
	}
	if !os.IsNotExist(err) {
		return wgtunnel.Key{}, false, fmt.Errorf("read tunnel key: %w", err)
	}
	key, err := wgtunnel.GenerateKey()
	if err != nil {
		return wgtunnel.Key{}, false, err
	}
	if err := os.WriteFile(path, []byte(key.String()+"\n"), 0o600); err != nil {
		return wgtunnel.Key{}, false, fmt.Errorf("write tunnel key: %w", err)
	}
	return key, true, nil
}

func postEnroll(ctx context.Context, opts EnrollOptions, publicKey wgtunnel.Key) (wire.TunnelEnrollResponse, error) {
	body, err := json.Marshal(wire.TunnelEnrollRequest{PublicKey: publicKey.String()})
	if err != nil {
		return wire.TunnelEnrollResponse{}, err
	}
	url := strings.TrimRight(opts.ServerURL, "/") + "/api/v1/agent/tunnel"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return wire.TunnelEnrollResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", opts.Token)

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.InsecureSkip},
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return wire.TunnelEnrollResponse{}, fmt.Errorf("tunnel enroll request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(data))
		return wire.TunnelEnrollResponse{}, fmt.Errorf("tunnel enroll failed: status %d: %s", resp.StatusCode, detail)
	}
	var enrolled wire.TunnelEnrollResponse
	if err := json.Unmarshal(data, &enrolled); err != nil {
		return wire.TunnelEnrollResponse{}, fmt.Errorf("tunnel enroll response: %w", err)
	}
	return enrolled, nil
}

func getJSON(ctx context.Context, opts EnrollOptions, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(opts.ServerURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Agent-Token", opts.Token)
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.InsecureSkip},
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

func writeTunnelSettings(configPath string, settings TunnelSettings) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read backup config %s: %w", configPath, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse backup config %s: %w", configPath, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	tunnelDoc := map[string]any{
		"private_key_file":  settings.PrivateKeyFile,
		"server_public_key": settings.ServerPublicKey,
		"endpoint":          settings.Endpoint,
		"local_ip":          settings.LocalIP,
		"server_ip":         settings.ServerIP,
		"rest_port":         settings.RestPort,
	}
	if len(settings.Nodes) > 0 {
		nodeDocs := make([]map[string]any, 0, len(settings.Nodes))
		for _, node := range settings.Nodes {
			nodeDocs = append(nodeDocs, map[string]any{
				"host":       node.Host,
				"public_key": node.PublicKey,
				"endpoint":   node.Endpoint,
				"ip":         node.IP,
				"rest_port":  node.RestPort,
			})
		}
		tunnelDoc["node"] = nodeDocs
	}
	doc["tunnel"] = tunnelDoc
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return fmt.Errorf("encode backup config: %w", err)
	}
	info, err := os.Stat(configPath)
	mode := os.FileMode(0o640)
	if err == nil {
		mode = info.Mode().Perm()
	}
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), mode); err != nil {
		return fmt.Errorf("write backup config: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace backup config: %w", err)
	}
	return nil
}
