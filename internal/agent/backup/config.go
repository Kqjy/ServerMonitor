package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	StatusPath    string
	ResticPath    string
	Paths         []string
	Excludes      []string
	OneFileSystem bool
	PruneMode     string
	Retention     Retention
	Repos         []Repo
	Tunnel        *TunnelSettings
	Schedule      *ScheduleSettings
	Warnings      []string
}

type ScheduleSettings struct {
	Enabled             bool
	BackupTime          string
	BackupJitterS       int
	CheckWeekday        string
	CheckTime           string
	CheckJitterS        int
	CheckReadDataSubset string
}

type TunnelSettings struct {
	PrivateKeyFile  string               `toml:"private_key_file"`
	ServerPublicKey string               `toml:"server_public_key"`
	Endpoint        string               `toml:"endpoint"`
	LocalIP         string               `toml:"local_ip"`
	ServerIP        string               `toml:"server_ip"`
	RestPort        int                  `toml:"rest_port"`
	MTU             int                  `toml:"mtu"`
	Nodes           []TunnelNodeSettings `toml:"node"`
}

type TunnelNodeSettings struct {
	Host      string `toml:"host"`
	PublicKey string `toml:"public_key"`
	Endpoint  string `toml:"endpoint"`
	IP        string `toml:"ip"`
	RestPort  int    `toml:"rest_port"`
}

func (t *TunnelSettings) Node(host string) *TunnelNodeSettings {
	for i := range t.Nodes {
		if t.Nodes[i].Host == host {
			return &t.Nodes[i]
		}
	}
	return nil
}

type Retention struct {
	Daily   int
	Weekly  int
	Monthly int
}

type RetentionOverride struct {
	Daily   *int `toml:"daily"`
	Weekly  *int `toml:"weekly"`
	Monthly *int `toml:"monthly"`
}

type Repo struct {
	Name         string             `toml:"name"`
	URL          string             `toml:"url"`
	TunnelName   string             `toml:"tunnel_name"`
	TunnelNode   string             `toml:"tunnel_node"`
	PasswordFile string             `toml:"password_file"`
	EnvFile      string             `toml:"env_file"`
	S3Region     string             `toml:"s3_region"`
	S3PathStyle  bool               `toml:"s3_path_style"`
	Retention    *RetentionOverride `toml:"retention"`

	managed   bool
	tunnelErr error
}

func (r Repo) UsesTunnel() bool { return r.TunnelName != "" }

func (c Config) HasTunnelRepos() bool {
	for _, repo := range c.Repos {
		if repo.UsesTunnel() {
			return true
		}
	}
	return false
}

var allowedRepoEnvKeys = map[string]bool{
	"AWS_ACCESS_KEY_ID":     true,
	"AWS_SECRET_ACCESS_KEY": true,
	"AWS_SESSION_TOKEN":     true,
	"AWS_DEFAULT_REGION":    true,
	"B2_ACCOUNT_ID":         true,
	"B2_ACCOUNT_KEY":        true,
	"RESTIC_REST_USERNAME":  true,
	"RESTIC_REST_PASSWORD":  true,
}

func loadRepoEnv(repo Repo) (map[string]string, error) {
	out := map[string]string{}
	if repo.S3Region != "" {
		out["AWS_DEFAULT_REGION"] = repo.S3Region
	}
	if repo.EnvFile == "" {
		return out, nil
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(repo.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("stat repo credentials for %q: %w", repo.Name, err)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("repo credentials for %q: %s is group- or world-writable; must be root-owned (0640 or stricter)", repo.Name, repo.EnvFile)
		}
	}
	data, err := os.ReadFile(repo.EnvFile)
	if err != nil {
		return nil, fmt.Errorf("read repo credentials for %q: %w", repo.Name, err)
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("repo credentials for %q: expected KEY=VALUE lines", repo.Name)
		}
		key = strings.TrimSpace(key)
		if !allowedRepoEnvKeys[key] {
			return nil, fmt.Errorf("repo credentials for %q: key %q is not allowed", repo.Name, key)
		}
		out[key] = strings.TrimSpace(value)
	}
	return out, nil
}

func repoEnvSecretValues(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		if value == "" || key == "AWS_DEFAULT_REGION" {
			continue
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return len(out[i]) > len(out[j])
	})
	return out
}

type rawConfig struct {
	StatusPath    string          `toml:"status_path"`
	ResticPath    string          `toml:"restic_path"`
	Paths         []string        `toml:"paths"`
	Excludes      []string        `toml:"excludes"`
	OneFileSystem bool            `toml:"one_file_system"`
	PruneMode     string          `toml:"prune_mode"`
	Retention     *rawRetention   `toml:"retention"`
	Repos         []Repo          `toml:"repo"`
	Tunnel        *TunnelSettings `toml:"tunnel"`
	Schedule      *rawSchedule    `toml:"schedule"`
}

type rawSchedule struct {
	Enabled             bool   `toml:"enabled"`
	BackupTime          string `toml:"backup_time"`
	BackupJitterS       int    `toml:"backup_jitter_s"`
	CheckWeekday        string `toml:"check_weekday"`
	CheckTime           string `toml:"check_time"`
	CheckJitterS        int    `toml:"check_jitter_s"`
	CheckReadDataSubset string `toml:"check_read_data_subset"`
}

type rawRetention = RetentionOverride

func DefaultConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("SM_BACKUP_CONFIG")); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if pd := strings.TrimSpace(os.Getenv("ProgramData")); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "Backup", "backup.toml")
		}
		return `C:\ProgramData\ServerMonitor\Backup\backup.toml`
	}
	return "/etc/servermonitor-backup/backup.toml"
}

func DefaultStatusPath() string {
	if v := strings.TrimSpace(os.Getenv("SM_BACKUP_STATUS_PATH")); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if pd := strings.TrimSpace(os.Getenv("ProgramData")); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "backup-status.json")
		}
		return `C:\ProgramData\ServerMonitor\backup-status.json`
	}
	return "/var/lib/servermonitor/backup-status.json"
}

func Load(path string) (Config, error) {
	return loadConfig(path, false)
}

func LoadForScheduling(path string) (Config, error) {
	return loadConfig(path, true)
}

func loadConfig(path string, allowUnenrolledTunnel bool) (Config, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read backup config %s: %w", path, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse backup config %s: %w", path, err)
	}
	cfg := Config{
		StatusPath:    strings.TrimSpace(raw.StatusPath),
		ResticPath:    strings.TrimSpace(raw.ResticPath),
		Paths:         trimmedList(raw.Paths),
		Excludes:      trimmedList(raw.Excludes),
		OneFileSystem: raw.OneFileSystem,
		PruneMode:     strings.TrimSpace(raw.PruneMode),
		Retention:     defaultRetention(raw.Retention),
		Repos:         trimmedRepos(raw.Repos),
		Tunnel:        trimmedTunnel(raw.Tunnel),
		Schedule:      parseSchedule(raw.Schedule),
	}
	if cfg.StatusPath == "" {
		cfg.StatusPath = DefaultStatusPath()
	}
	if cfg.PruneMode == "" {
		cfg.PruneMode = "host"
	}
	passwordFile := filepath.Join(filepath.Dir(path), "backup.key")
	if len(cfg.Repos) > 0 && strings.TrimSpace(cfg.Repos[0].PasswordFile) != "" {
		passwordFile = strings.TrimSpace(cfg.Repos[0].PasswordFile)
	}
	managedRepos, err := loadManagedRepos(cfg.StatusPath, passwordFile)
	if err != nil {
		return Config{}, fmt.Errorf("load centrally managed repositories: %w", err)
	}
	local := make(map[string]bool, len(cfg.Repos))
	for _, repo := range cfg.Repos {
		local[repo.Name] = true
	}
	for _, repo := range managedRepos {
		if local[repo.Name] {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("centrally managed repository %q is skipped: %s already defines a repository with that name; rename one of them, or remove the local [[repo]] block to adopt the managed one", repo.Name, path))
			continue
		}
		cfg.Repos = append(cfg.Repos, repo)
	}
	if err := cfg.validate(allowUnenrolledTunnel); err != nil {
		return Config{}, fmt.Errorf("validate backup config %s: %w", path, err)
	}
	return cfg, nil
}

func parseSchedule(raw *rawSchedule) *ScheduleSettings {
	if raw == nil {
		return nil
	}
	return &ScheduleSettings{
		Enabled:             raw.Enabled,
		BackupTime:          strings.TrimSpace(raw.BackupTime),
		BackupJitterS:       raw.BackupJitterS,
		CheckWeekday:        strings.TrimSpace(raw.CheckWeekday),
		CheckTime:           strings.TrimSpace(raw.CheckTime),
		CheckJitterS:        raw.CheckJitterS,
		CheckReadDataSubset: strings.TrimSpace(raw.CheckReadDataSubset),
	}
}

func (c Config) Validate() error {
	return c.validate(false)
}

func (c Config) validate(allowUnenrolledTunnel bool) error {
	if strings.TrimSpace(c.StatusPath) == "" {
		return fmt.Errorf("status_path is required")
	}
	if len(c.Paths) == 0 {
		return fmt.Errorf("at least one path is required")
	}
	for i, path := range c.Paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("paths[%d] is empty", i)
		}
	}
	pruneMode := strings.TrimSpace(c.PruneMode)
	if pruneMode == "" {
		pruneMode = "host"
	}
	if pruneMode != "host" && pruneMode != "external" {
		return fmt.Errorf("prune_mode must be \"host\" or \"external\"")
	}
	if err := validateRetention(c.Retention); err != nil {
		return err
	}
	if len(c.Repos) == 0 {
		if !ManagedBackupConfigExists(c.StatusPath) {
			return fmt.Errorf("at least one repo is required")
		}
	}
	seen := map[string]bool{}
	for i, repo := range c.Repos {
		if strings.TrimSpace(repo.Name) == "" {
			return fmt.Errorf("repo[%d].name is required", i)
		}
		hasURL := strings.TrimSpace(repo.URL) != ""
		hasTunnel := strings.TrimSpace(repo.TunnelName) != ""
		if hasURL == hasTunnel {
			return fmt.Errorf("repo[%s]: exactly one of url or tunnel_name is required", repo.Name)
		}
		if hasURL && strings.HasPrefix(strings.ToLower(strings.TrimSpace(repo.URL)), "tunnel:") {
			return fmt.Errorf("repo[%s]: url uses the tunnel: scheme, which is not a restic backend; tunnel repositories must use tunnel_name (and tunnel_node for a node), then run: sm-agent backup tunnel-enroll (or reinstall with a current installer)", repo.Name)
		}
		if hasTunnel && c.Tunnel == nil && !allowUnenrolledTunnel {
			return fmt.Errorf("repo[%s].tunnel_name requires a [tunnel] section (run: sm-agent backup tunnel-enroll)", repo.Name)
		}
		if strings.TrimSpace(repo.PasswordFile) == "" {
			return fmt.Errorf("repo[%s].password_file is required", repo.Name)
		}
		if repo.EnvFile != "" && !filepath.IsAbs(repo.EnvFile) {
			return fmt.Errorf("repo[%s].env_file must be an absolute path", repo.Name)
		}
		if (repo.S3Region != "" || repo.S3PathStyle) && !strings.HasPrefix(repo.URL, "s3:") {
			return fmt.Errorf("repo[%s].s3_region/s3_path_style require an s3: url", repo.Name)
		}
		if hasTunnel && !tunnelRepoNameRE.MatchString(repo.TunnelName) {
			return fmt.Errorf("repo[%s].tunnel_name must match %s", repo.Name, tunnelRepoNameRE)
		}
		if repo.TunnelNode != "" && !hasTunnel {
			return fmt.Errorf("repo[%s].tunnel_node requires tunnel_name", repo.Name)
		}
		if repo.TunnelNode != "" && c.Tunnel != nil && c.Tunnel.Node(repo.TunnelNode) == nil && !allowUnenrolledTunnel {
			return fmt.Errorf("repo[%s].tunnel_node %q has no [[tunnel.node]] entry (re-run: sm-agent backup tunnel-enroll)", repo.Name, repo.TunnelNode)
		}
		if err := validateRetention(effectiveRetention(c.Retention, repo.Retention)); err != nil {
			return fmt.Errorf("repo[%s].retention: %w", repo.Name, err)
		}
		if seen[repo.Name] {
			return fmt.Errorf("repo name %q is duplicated", repo.Name)
		}
		seen[repo.Name] = true
	}
	if c.Tunnel != nil && !allowUnenrolledTunnel {
		if err := c.Tunnel.validate(c.needsServerTunnel()); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) needsServerTunnel() bool {
	for _, repo := range c.Repos {
		if repo.UsesTunnel() && repo.TunnelNode == "" {
			return true
		}
	}
	return false
}

func (t *TunnelSettings) validate(needServer bool) error {
	if strings.TrimSpace(t.PrivateKeyFile) == "" {
		return fmt.Errorf("tunnel.private_key_file is required")
	}
	if strings.TrimSpace(t.LocalIP) == "" {
		return fmt.Errorf("tunnel.local_ip is required")
	}
	if needServer {
		if strings.TrimSpace(t.ServerPublicKey) == "" {
			return fmt.Errorf("tunnel.server_public_key is required")
		}
		if strings.TrimSpace(t.Endpoint) == "" {
			return fmt.Errorf("tunnel.endpoint is required")
		}
		if strings.TrimSpace(t.ServerIP) == "" {
			return fmt.Errorf("tunnel.server_ip is required")
		}
		if t.RestPort <= 0 || t.RestPort > 65535 {
			return fmt.Errorf("tunnel.rest_port must be between 1 and 65535")
		}
	}
	if t.MTU != 0 && (t.MTU < 576 || t.MTU > 1420) {
		return fmt.Errorf("tunnel.mtu must be between 576 and 1420")
	}
	for _, node := range t.Nodes {
		if strings.TrimSpace(node.Host) == "" {
			return fmt.Errorf("tunnel.node.host is required")
		}
		if strings.TrimSpace(node.PublicKey) == "" || strings.TrimSpace(node.Endpoint) == "" || strings.TrimSpace(node.IP) == "" {
			return fmt.Errorf("tunnel.node[%s]: public_key, endpoint and ip are required", node.Host)
		}
		if node.RestPort <= 0 || node.RestPort > 65535 {
			return fmt.Errorf("tunnel.node[%s].rest_port must be between 1 and 65535", node.Host)
		}
	}
	return nil
}

func normalizedConfig(c Config) Config {
	c.StatusPath = strings.TrimSpace(c.StatusPath)
	if c.StatusPath == "" {
		c.StatusPath = DefaultStatusPath()
	}
	c.ResticPath = strings.TrimSpace(c.ResticPath)
	c.Paths = trimmedList(c.Paths)
	c.Excludes = trimmedList(c.Excludes)
	c.PruneMode = strings.TrimSpace(c.PruneMode)
	if c.PruneMode == "" {
		c.PruneMode = "host"
	}
	c.Repos = trimmedRepos(c.Repos)
	c.Tunnel = trimmedTunnel(c.Tunnel)
	return c
}

func defaultRetention(raw *rawRetention) Retention {
	retention := Retention{Daily: 7, Weekly: 4, Monthly: 6}
	if raw == nil {
		return retention
	}
	if raw.Daily != nil {
		retention.Daily = *raw.Daily
	}
	if raw.Weekly != nil {
		retention.Weekly = *raw.Weekly
	}
	if raw.Monthly != nil {
		retention.Monthly = *raw.Monthly
	}
	return retention
}

func trimmedList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func trimmedRepos(repos []Repo) []Repo {
	out := make([]Repo, 0, len(repos))
	for _, repo := range repos {
		out = append(out, Repo{
			Name:         strings.TrimSpace(repo.Name),
			URL:          strings.TrimSpace(repo.URL),
			TunnelName:   strings.TrimSpace(repo.TunnelName),
			TunnelNode:   strings.TrimSpace(repo.TunnelNode),
			PasswordFile: strings.TrimSpace(repo.PasswordFile),
			EnvFile:      strings.TrimSpace(repo.EnvFile),
			S3Region:     strings.TrimSpace(repo.S3Region),
			S3PathStyle:  repo.S3PathStyle,
			Retention:    repo.Retention,
			managed:      repo.managed,
		})
	}
	return out
}

func trimmedTunnel(t *TunnelSettings) *TunnelSettings {
	if t == nil {
		return nil
	}
	nodes := make([]TunnelNodeSettings, 0, len(t.Nodes))
	for _, node := range t.Nodes {
		nodes = append(nodes, TunnelNodeSettings{
			Host:      strings.TrimSpace(node.Host),
			PublicKey: strings.TrimSpace(node.PublicKey),
			Endpoint:  strings.TrimSpace(node.Endpoint),
			IP:        strings.TrimSpace(node.IP),
			RestPort:  node.RestPort,
		})
	}
	return &TunnelSettings{
		PrivateKeyFile:  strings.TrimSpace(t.PrivateKeyFile),
		ServerPublicKey: strings.TrimSpace(t.ServerPublicKey),
		Endpoint:        strings.TrimSpace(t.Endpoint),
		LocalIP:         strings.TrimSpace(t.LocalIP),
		ServerIP:        strings.TrimSpace(t.ServerIP),
		RestPort:        t.RestPort,
		MTU:             t.MTU,
		Nodes:           nodes,
	}
}

var tunnelRepoNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func effectiveRetention(base Retention, override *RetentionOverride) Retention {
	retention := base
	if override == nil {
		return retention
	}
	if override.Daily != nil {
		retention.Daily = *override.Daily
	}
	if override.Weekly != nil {
		retention.Weekly = *override.Weekly
	}
	if override.Monthly != nil {
		retention.Monthly = *override.Monthly
	}
	return retention
}

func validateRetention(retention Retention) error {
	if retention.Daily < 0 || retention.Weekly < 0 || retention.Monthly < 0 {
		return fmt.Errorf("retention values must be >= 0")
	}
	if retention.Daily == 0 && retention.Weekly == 0 && retention.Monthly == 0 {
		return fmt.Errorf("at least one retention value must be > 0")
	}
	return nil
}
