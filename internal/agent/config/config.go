package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ServerURL        string            `toml:"server_url"`
	Token            string            `toml:"token"`
	ServerPubkey     string            `toml:"server_pubkey"`
	IntervalS        int               `toml:"interval_s"`
	SmartSampleS     int               `toml:"smart_sample_s"`
	Enabled          []string          `toml:"enabled"`
	Disabled         []string          `toml:"disabled"`
	ProcessTopN      int               `toml:"process_top_n"`
	BatchMaxAgeS     int               `toml:"batch_max_age_s"`
	BatchMaxPoints   int               `toml:"batch_max_points"`
	SpoolPath        string            `toml:"spool_path"`
	SpoolMaxBytes    int64             `toml:"spool_max_bytes"`
	HealthPath       string            `toml:"health_path"`
	BackupStatusPath string            `toml:"backup_status_path"`
	HTTPTimeout      time.Duration     `toml:"http_timeout"`
	InsecureSkip     bool              `toml:"insecure_skip_verify"`
	EnableIPBan      bool              `toml:"enable_ipban"`
	AutoUpgrade      *bool             `toml:"auto_upgrade"`
	Tags             map[string]string `toml:"tags"`
}

func Load(path string) (*Config, error) {
	c := &Config{
		IntervalS:        10,
		ProcessTopN:      50,
		BatchMaxAgeS:     5,
		BatchMaxPoints:   10000,
		SpoolMaxBytes:    256 * 1024 * 1024,
		BackupStatusPath: defaultBackupStatusPath(),
		HTTPTimeout:      20 * time.Second,
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read config: %w", err)
	}

	applyEnvOverrides(c)

	if c.ServerURL == "" {
		return nil, fmt.Errorf("server_url is required (set it in %s or via SM_SERVER_URL)", path)
	}
	if c.Token == "" {
		return nil, fmt.Errorf("token is required (set it in %s or via SM_TOKEN)", path)
	}
	if c.IntervalS <= 0 {
		c.IntervalS = 10
	}
	return c, nil
}

func applyEnvOverrides(c *Config) {
	if v := os.Getenv("SM_SERVER_URL"); v != "" {
		c.ServerURL = v
	}
	if v := os.Getenv("SM_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("SM_SERVER_PUBKEY"); v != "" {
		c.ServerPubkey = v
	}
	if v := os.Getenv("SM_SPOOL_PATH"); v != "" {
		c.SpoolPath = v
	}
	if v := os.Getenv("SM_BACKUP_STATUS_PATH"); v != "" {
		c.BackupStatusPath = v
	}
	if v := os.Getenv("SM_INTERVAL_S"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.IntervalS = n
		}
	}
	if v := os.Getenv("SM_SMART_SAMPLE_S"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.SmartSampleS = n
		}
	}
	if v := os.Getenv("SM_AUTO_UPGRADE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.AutoUpgrade = &b
		}
	}
	if v := os.Getenv("SM_ENABLE_IPBAN"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.EnableIPBan = b
		}
	}
}

func defaultBackupStatusPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "backup-status.json")
		}
		return `C:\ProgramData\ServerMonitor\backup-status.json`
	}
	return "/var/lib/servermonitor/backup-status.json"
}

func (c *Config) Interval() time.Duration {
	return time.Duration(c.IntervalS) * time.Second
}

func (c *Config) AutoUpgradeEnabled() bool {
	if c.AutoUpgrade == nil {
		return true
	}
	return *c.AutoUpgrade
}

func (c *Config) BatchMaxAge() time.Duration {
	return time.Duration(c.BatchMaxAgeS) * time.Second
}
