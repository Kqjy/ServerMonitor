package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ServerURL      string            `toml:"server_url"`
	Token          string            `toml:"token"`
	IntervalS      int               `toml:"interval_s"`
	Enabled        []string          `toml:"enabled"`
	Disabled       []string          `toml:"disabled"`
	ProcessTopN    int               `toml:"process_top_n"`
	BatchMaxAgeS   int               `toml:"batch_max_age_s"`
	BatchMaxPoints int               `toml:"batch_max_points"`
	SpoolPath      string            `toml:"spool_path"`
	SpoolMaxBytes  int64             `toml:"spool_max_bytes"`
	HTTPTimeout    time.Duration     `toml:"http_timeout"`
	InsecureSkip   bool              `toml:"insecure_skip_verify"`
	AutoUpgrade    *bool             `toml:"auto_upgrade"`
	Tags           map[string]string `toml:"tags"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c := &Config{
		IntervalS:      10,
		ProcessTopN:    50,
		BatchMaxAgeS:   5,
		BatchMaxPoints: 10000,
		SpoolMaxBytes:  256 * 1024 * 1024,
		HTTPTimeout:    20 * time.Second,
	}
	if err := toml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.ServerURL == "" {
		return nil, fmt.Errorf("server_url is required")
	}
	if c.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if c.IntervalS <= 0 {
		c.IntervalS = 10
	}
	return c, nil
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
