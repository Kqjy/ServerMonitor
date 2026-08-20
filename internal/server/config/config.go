package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr             string
	DatabaseURL          string
	AdminToken           string
	S3Bucket             string
	S3Region             string
	S3Prefix             string
	S3Endpoint           string
	S3UsePathStyle       bool
	BackupDir            string
	BackupS3Bucket       string
	BackupS3Region       string
	BackupS3Prefix       string
	BackupS3Endpoint     string
	BackupS3UsePathStyle bool
	BackupMaxBlobBytes   int64
	BackupWGPort         int
	BackupWGEndpoint     string
	BackupWGSubnet       string
	BackupWGMTU          int
	BackupPublicHTTP     bool
	BackupACMEDomain     string
	ACMEEmail            string
	ACMECacheDir         string
	IngestRateLimit      int
	IngestBurst          int
	BatcherMaxRows       int
	BatcherMaxAge        time.Duration
	LogLevel             string
	RetentionRaw         string
	RetentionAggregate5m string
	RetentionProcesses   string
	RetentionContainers  string
	RetentionPorts       string
	CompressionAfter     string
	TrustedProxies       []*net.IPNet
	TLSCertFile          string
	TLSKeyFile           string
	TrustProxyTLS        bool
	InsecureAllowHTTP    bool
	AgentSigningKeyFile  string
}

func (c *Config) ServesTLS() bool      { return c.TLSCertFile != "" && c.TLSKeyFile != "" }
func (c *Config) BehindTLSProxy() bool { return c.TrustProxyTLS }
func (c *Config) SecureBrowserSide() bool {
	return c.ServesTLS() || c.BehindTLSProxy() || c.ACMEEnabled()
}

func (c *Config) BackupEnabled() bool { return c.BackupDir != "" || c.BackupS3Bucket != "" }

func (c *Config) BackupTunnelEnabled() bool { return c.BackupWGPort > 0 }

func (c *Config) BackupServesPublicHTTP() bool {
	return c.BackupEnabled() && (!c.BackupTunnelEnabled() || c.BackupPublicHTTP)
}

func (c *Config) ACMEEnabled() bool {
	return c.BackupACMEDomain != "" && !c.ServesTLS() && !c.TrustProxyTLS
}

func (c *Config) BackupTLSMode() string {
	switch {
	case c.ServesTLS():
		return "tls"
	case c.TrustProxyTLS:
		return "proxy"
	case c.ACMEEnabled():
		return "acme"
	default:
		return "insecure"
	}
}

func (c *Config) BackupTLSSecure() bool { return c.BackupTLSMode() != "insecure" }

func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:             getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:          getenv("DATABASE_URL", ""),
		AdminToken:           getenv("ADMIN_TOKEN", ""),
		S3Bucket:             getenv("S3_BUCKET", ""),
		S3Region:             getenv("S3_REGION", ""),
		S3Prefix:             getenv("S3_PREFIX", "metrics"),
		S3Endpoint:           getenv("S3_ENDPOINT", ""),
		S3UsePathStyle:       getenvBool("S3_USE_PATH_STYLE", false),
		BackupDir:            getenv("BACKUP_DIR", ""),
		BackupS3Bucket:       getenv("BACKUP_S3_BUCKET", ""),
		BackupS3Region:       getenv("BACKUP_S3_REGION", ""),
		BackupS3Prefix:       getenv("BACKUP_S3_PREFIX", "backups"),
		BackupS3Endpoint:     getenv("BACKUP_S3_ENDPOINT", ""),
		BackupS3UsePathStyle: getenvBool("BACKUP_S3_USE_PATH_STYLE", false),
		BackupMaxBlobBytes:   getenvInt64("BACKUP_MAX_BLOB_BYTES", 1<<30),
		BackupWGPort:         getenvInt("BACKUP_WG_PORT", 0),
		BackupWGEndpoint:     getenv("BACKUP_WG_ENDPOINT", ""),
		BackupWGSubnet:       getenv("BACKUP_WG_SUBNET", "10.83.0.0/16"),
		BackupWGMTU:          getenvInt("BACKUP_WG_MTU", 1280),
		BackupPublicHTTP:     getenvBool("BACKUP_PUBLIC_HTTP", false),
		BackupACMEDomain:     getenv("BACKUP_ACME_DOMAIN", ""),
		ACMEEmail:            getenv("ACME_EMAIL", ""),
		ACMECacheDir:         getenv("ACME_CACHE_DIR", ""),
		IngestRateLimit:      getenvInt("INGEST_RATE_LIMIT", 10),
		IngestBurst:          getenvInt("INGEST_BURST", 30),
		BatcherMaxRows:       getenvInt("BATCHER_MAX_ROWS", 50000),
		BatcherMaxAge:        getenvDuration("BATCHER_MAX_AGE", 2*time.Second),
		LogLevel:             getenv("LOG_LEVEL", "info"),
		RetentionRaw:         getenv("RETENTION_RAW", "30 days"),
		RetentionAggregate5m: getenv("RETENTION_AGGREGATE_5M", "6 months"),
		RetentionProcesses:   getenv("RETENTION_PROCESSES", "7 days"),
		RetentionContainers:  getenv("RETENTION_CONTAINERS", "30 days"),
		RetentionPorts:       getenv("RETENTION_PORTS", "30 days"),
		CompressionAfter:     getenv("COMPRESSION_AFTER", "7 days"),
		TLSCertFile:          getenv("TLS_CERT_FILE", ""),
		TLSKeyFile:           getenv("TLS_KEY_FILE", ""),
		TrustProxyTLS:        getenvBool("TRUST_PROXY_TLS", false),
		InsecureAllowHTTP:    getenvBool("INSECURE_ALLOW_HTTP", false),
		AgentSigningKeyFile:  getenv("AGENT_SIGNING_KEY_FILE", ""),
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.AdminToken == "" {
		return nil, fmt.Errorf("ADMIN_TOKEN is required (used to register agents and access the UI)")
	}
	if looksLikePlaceholder(c.AdminToken) {
		return nil, fmt.Errorf("ADMIN_TOKEN looks like the .env.example placeholder; generate a strong random value (e.g. openssl rand -hex 32) before starting")
	}
	if databaseURLHasPlaceholderPassword(c.DatabaseURL) {
		return nil, fmt.Errorf("DATABASE_URL password looks like the .env.example placeholder; set POSTGRES_PASSWORD to a strong random value before starting")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return nil, fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}
	if !c.SecureBrowserSide() && !c.InsecureAllowHTTP {
		return nil, fmt.Errorf("refusing to start over plaintext HTTP: set TLS_CERT_FILE+TLS_KEY_FILE for native TLS, TRUST_PROXY_TLS=1 if a reverse proxy terminates TLS, BACKUP_ACME_DOMAIN for automatic Let's Encrypt, or INSECURE_ALLOW_HTTP=1 to acknowledge plaintext (loopback/dev only)")
	}
	if c.BackupDir != "" && c.BackupS3Bucket != "" {
		return nil, fmt.Errorf("set BACKUP_DIR or BACKUP_S3_BUCKET, not both")
	}
	if c.BackupWGPort != 0 {
		if c.BackupWGPort < 1 || c.BackupWGPort > 65535 {
			return nil, fmt.Errorf("BACKUP_WG_PORT must be between 1 and 65535")
		}
		prefix, err := netip.ParsePrefix(c.BackupWGSubnet)
		if err != nil {
			return nil, fmt.Errorf("BACKUP_WG_SUBNET: %w", err)
		}
		if prefix.Addr().Is4() && prefix.Bits() > 30 {
			return nil, fmt.Errorf("BACKUP_WG_SUBNET %s is too small; use /30 or larger", c.BackupWGSubnet)
		}
		if c.BackupWGMTU < 576 || c.BackupWGMTU > 1420 {
			return nil, fmt.Errorf("BACKUP_WG_MTU must be between 576 and 1420")
		}
		if c.BackupWGEndpoint != "" {
			host := c.BackupWGEndpoint
			if h, _, splitErr := net.SplitHostPort(c.BackupWGEndpoint); splitErr == nil {
				host = h
			}
			if strings.TrimSpace(host) == "" {
				return nil, fmt.Errorf("BACKUP_WG_ENDPOINT must be host or host:port")
			}
		}
	}
	if c.BackupMaxBlobBytes <= 0 {
		c.BackupMaxBlobBytes = 1 << 30
	}
	for _, p := range []struct {
		name, value string
	}{
		{"RETENTION_RAW", c.RetentionRaw},
		{"RETENTION_AGGREGATE_5M", c.RetentionAggregate5m},
		{"RETENTION_PROCESSES", c.RetentionProcesses},
		{"RETENTION_CONTAINERS", c.RetentionContainers},
		{"RETENTION_PORTS", c.RetentionPorts},
		{"COMPRESSION_AFTER", c.CompressionAfter},
	} {
		if err := ValidateInterval(p.value); err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
	}
	nets, err := parseTrustedProxies(getenv("TRUSTED_PROXIES", ""))
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	c.TrustedProxies = nets
	return c, nil
}

func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	nets := make([]*net.IPNet, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			ip := net.ParseIP(p)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP %q", p)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			p = fmt.Sprintf("%s/%d", p, bits)
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", p, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

var intervalRE = regexp.MustCompile(`^(\d+)\s+(second|minute|hour|day|week|month|year)s?$`)

func ValidateInterval(v string) error {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "forever") {
		return nil
	}
	if !intervalRE.MatchString(v) {
		return fmt.Errorf("must be empty, \"forever\", or N <unit> where unit is second|minute|hour|day|week|month|year (got %q)", v)
	}
	return nil
}

func IsForever(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || strings.EqualFold(v, "forever")
}

const intervalForeverDuration = 100 * 365 * 24 * time.Hour

func IntervalToDuration(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "forever") {
		return intervalForeverDuration
	}
	m := intervalRE.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	switch m[2] {
	case "second":
		return time.Duration(n) * time.Second
	case "minute":
		return time.Duration(n) * time.Minute
	case "hour":
		return time.Duration(n) * time.Hour
	case "day":
		return time.Duration(n) * 24 * time.Hour
	case "week":
		return time.Duration(n) * 7 * 24 * time.Hour
	case "month":
		return time.Duration(n) * 30 * 24 * time.Hour
	case "year":
		return time.Duration(n) * 365 * 24 * time.Hour
	}
	return 0
}

func looksLikePlaceholder(v string) bool {
	lower := strings.ToLower(v)
	for _, marker := range []string{"change-me", "changeme", "change_me", "replace-me", "your-token-here", "your_password_here"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func databaseURLHasPlaceholderPassword(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	pw, ok := u.User.Password()
	if !ok {
		return false
	}
	return looksLikePlaceholder(pw)
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func EnvBool(key string, def bool) bool { return getenvBool(key, def) }

func getenvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
