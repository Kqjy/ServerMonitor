package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"servermonitor/internal/agent/collectors"
	"servermonitor/internal/agent/config"
	"servermonitor/internal/agent/runner"
	"servermonitor/internal/agent/spool"
	"servermonitor/internal/agent/transport"
	"servermonitor/internal/agent/upgrade"
	"servermonitor/pkg/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "register" {
		registerCmd(os.Args[2:])
		return
	}

	var (
		configPath = flag.String("config", defaultConfigPath(), "path to agent.toml")
		showVer    = flag.Bool("version", false, "print version and exit")
		listCols   = flag.Bool("list-collectors", false, "list available collectors for this platform and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("sm-agent " + runner.Version)
		return
	}

	if *listCols {
		for _, c := range collectors.Filtered(nil, nil) {
			fmt.Printf("%-14s  platforms=%v\n", c.Name(), c.Platforms())
		}
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	sentinel := deregisteredSentinelPath(*configPath)
	if data, err := os.ReadFile(sentinel); err == nil {
		logger.Error("agent disabled by server deregistration",
			"sentinel", sentinel,
			"detail", string(data),
			"hint", "delete the sentinel file to re-arm, or re-register with sm-agent register")
		os.Exit(exitCodeDeregistered)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config", "path", *configPath, "err", err)
		os.Exit(1)
	}

	if cfg.InsecureSkip {
		logger.Warn("insecure_skip_verify=true: TLS certificate verification is DISABLED - never use in production",
			"config", *configPath,
			"server_url", cfg.ServerURL)
	}
	if strings.HasPrefix(strings.ToLower(cfg.ServerURL), "http://") {
		logger.Warn("server_url uses http://: agent token will be sent over plaintext - never use in production",
			"config", *configPath,
			"server_url", cfg.ServerURL)
	}

	var sp *spool.Spool
	if cfg.SpoolPath != "" {
		sp, err = spool.Open(cfg.SpoolPath, cfg.SpoolMaxBytes)
		if err != nil {
			logger.Warn("spool unavailable", "path", cfg.SpoolPath, "err", err)
		} else {
			defer sp.Close()
		}
	}

	client := transport.New(cfg.ServerURL, cfg.Token, cfg.HTTPTimeout, cfg.InsecureSkip, logger, sp)
	client.SetCurrentInterval(cfg.IntervalS)
	r := runner.New(cfg, client, logger)

	pk := &pubkeyHolder{v: cfg.ServerPubkey}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client.StartDrainer(ctx)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-client.IntervalUpdates():
				if !ok {
					return
				}
				if err := persistInterval(*configPath, s); err != nil {
					logger.Warn("persist interval failed", "err", err)
				} else {
					logger.Info("interval updated from server", "interval_s", s)
				}
				r.SetInterval(time.Duration(s) * time.Second)
			}
		}
	}()

	var upgradeStaged atomic.Bool
	var upgradeInflight atomic.Bool
	go func() {
		var (
			failedVersion string
			failedCount   int
			nextAttempt   time.Time
		)
		for {
			select {
			case <-ctx.Done():
				return
			case upd, ok := <-client.ControlUpdates():
				if !ok {
					return
				}
				if upd.ServerPubkey != "" && pk.setIfEmpty(upd.ServerPubkey) {
					if err := persistServerPubkey(*configPath, upd.ServerPubkey); err != nil {
						logger.Warn("pinned server pubkey in memory but failed to persist to agent.toml; will retry after next restart",
							"err", err,
							"config", *configPath)
					} else {
						logger.Info("pinned server pubkey from ingest ack",
							"pubkey", upd.ServerPubkey,
							"config", *configPath)
					}
				}
				latest := upd.LatestVersion
				if latest == "" || !version.IsNewer(latest, runner.Version) {
					continue
				}
				autoUpgrade := cfg.AutoUpgradeEnabled()
				if upd.AutoUpgrade != nil {
					autoUpgrade = *upd.AutoUpgrade
				}
				forced := upd.UpgradeNow
				if !autoUpgrade && !forced {
					logger.Info("agent upgrade available (auto_upgrade=false; not applying)",
						"current", runner.Version,
						"latest", latest)
					continue
				}
				if failedVersion != latest {
					failedVersion = ""
					failedCount = 0
					nextAttempt = time.Time{}
				}
				if !forced && !nextAttempt.IsZero() && time.Now().Before(nextAttempt) {
					continue
				}
				if !upgradeInflight.CompareAndSwap(false, true) {
					continue
				}
				logger.Info("agent upgrade available, downloading",
					"current", runner.Version,
					"latest", latest,
					"forced", forced)
				err := upgrade.Run(ctx, upgrade.Options{
					ServerURL:    cfg.ServerURL,
					Token:        cfg.Token,
					ServerPubkey: pk.get(),
					InsecureSkip: cfg.InsecureSkip,
					Logger:       logger,
				})
				if err != nil {
					failedVersion = latest
					failedCount++
					delay := upgradeBackoff(failedCount)
					nextAttempt = time.Now().Add(delay)
					logger.Error("agent upgrade failed",
						"err", err,
						"latest", latest,
						"attempt", failedCount,
						"retry_after", delay)
					upgradeInflight.Store(false)
					continue
				}
				upgradeStaged.Store(true)
				cancel()
				return
			}
		}
	}()

	if err := r.Run(ctx); err != nil {
		if errors.Is(err, runner.ErrDeregistered) {
			logger.Error("host deregistered by server, agent shutting down",
				"server", cfg.ServerURL,
				"sentinel", sentinel)
			writeDeregisteredSentinel(sentinel, cfg.ServerURL)
			if sp != nil {
				sp.Close()
				if rmErr := os.Remove(cfg.SpoolPath); rmErr != nil && !os.IsNotExist(rmErr) {
					logger.Warn("could not remove spool", "path", cfg.SpoolPath, "err", rmErr)
				}
			}
			os.Exit(exitCodeDeregistered)
		}
		logger.Error("runner", "err", err)
		os.Exit(1)
	}
	if upgradeStaged.Load() {
		logger.Info("exiting for service manager to restart with upgraded binary",
			"exit_code", exitCodeUpgrade)
		os.Exit(exitCodeUpgrade)
	}
}

const exitCodeDeregistered = 78
const exitCodeUpgrade = 75

func upgradeBackoff(attempt int) time.Duration {
	const base = time.Minute
	const ceiling = time.Hour
	if attempt < 1 {
		return base
	}
	shift := attempt - 1
	if shift > 30 {
		return ceiling
	}
	d := base << shift
	if d <= 0 || d > ceiling {
		return ceiling
	}
	return d
}

func deregisteredSentinelPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "deregistered")
}

func writeDeregisteredSentinel(path, serverURL string) {
	content := time.Now().UTC().Format(time.RFC3339) + " server=" + serverURL + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write([]byte(content))
}

func persistInterval(path string, intervalS int) error {
	return persistConfigField(path, "interval_s", fmt.Sprintf("interval_s = %d", intervalS))
}

func persistServerPubkey(path, pubkey string) error {
	return persistConfigField(path, "server_pubkey", fmt.Sprintf("server_pubkey = %q", pubkey))
}

var configFileMu sync.Mutex

func persistConfigField(path, key, replacement string) error {
	configFileMu.Lock()
	defer configFileMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, l := range lines {
		k, _, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == key {
			lines[i] = replacement
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, replacement)
	}
	return writeConfigAtomic(path, []byte(strings.Join(lines, "\n")), false)
}

type pubkeyHolder struct {
	mu sync.Mutex
	v  string
}

func (h *pubkeyHolder) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.v
}

func (h *pubkeyHolder) setIfEmpty(s string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.v != "" {
		return false
	}
	h.v = s
	return true
}

func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "agent.toml")
		}
		return `C:\ProgramData\ServerMonitor\agent.toml`
	}
	return "/etc/servermonitor/agent.toml"
}

func defaultSpoolPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "spool.db")
		}
		return `C:\ProgramData\ServerMonitor\spool.db`
	}
	return "/var/lib/servermonitor/spool.db"
}

func registerCmd(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	server := fs.String("server", "", "server URL (required, or set SM_SERVER_URL)")
	adminTokenFile := fs.String("admin-token-file", "", "read admin token from this file (mode 0600)")
	hostname := fs.String("hostname", "", "hostname (default: this machine)")
	intervalS := fs.Int("interval", 10, "sample interval seconds")
	configPath := fs.String("config", defaultConfigPath(), "where to write the agent config")
	insecure := fs.Bool("insecure", false, "skip TLS cert verification for the registration call only AND allow http:// (debug only)")
	persistInsecure := fs.Bool("persist-insecure", false, "also write insecure_skip_verify=true into the agent config (sticky; debug only)")
	_ = fs.Parse(args)

	if *server == "" {
		*server = os.Getenv("SM_SERVER_URL")
	}
	adminToken := os.Getenv("SM_ADMIN_TOKEN")
	if adminToken == "" && *adminTokenFile != "" {
		b, err := os.ReadFile(*adminTokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read admin-token-file:", err)
			os.Exit(2)
		}
		adminToken = strings.TrimSpace(string(b))
	}
	_ = os.Unsetenv("SM_ADMIN_TOKEN")

	if *server == "" || adminToken == "" {
		fmt.Fprintln(os.Stderr, "usage: sm-agent register --server URL [--admin-token-file PATH] [--hostname NAME] [--interval N] [--config PATH] [--insecure]")
		fmt.Fprintln(os.Stderr, "       admin token comes from SM_ADMIN_TOKEN env var or --admin-token-file; SM_SERVER_URL also honored.")
		os.Exit(2)
	}

	if !strings.HasPrefix(strings.ToLower(*server), "https://") {
		if !*insecure {
			fmt.Fprintln(os.Stderr, "refusing to send admin token over non-https:// URL; pass --insecure to override (debug only)")
			os.Exit(2)
		}
	}

	if *hostname == "" {
		h, _ := os.Hostname()
		*hostname = h
	}

	token, hostID, pubkey, err := callRegister(*server, adminToken, *hostname, *intervalS, *insecure)
	if err != nil {
		fmt.Fprintln(os.Stderr, "register failed:", err)
		os.Exit(1)
	}
	if pubkey == "" {
		fmt.Fprintln(os.Stderr, "warning: server did not return server_pubkey; agent self-upgrade will be disabled until you upgrade and re-register against a signed server build")
	}

	stickyInsecure := *insecure && *persistInsecure
	cfg := fmt.Sprintf(`server_url    = %q
token         = %q
server_pubkey = %q
interval_s    = %d
spool_path    = %q
insecure_skip_verify = %t
`, *server, token, pubkey, *intervalS, defaultSpoolPath(), stickyInsecure)

	cfgDir := filepath.Dir(*configPath)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "create dir:", err)
		os.Exit(1)
	}
	if err := lockACLSystemAdminsOnly(cfgDir); err != nil {
		fmt.Fprintln(os.Stderr, "lock config dir ACL:", err)
		os.Exit(1)
	}
	if err := writeConfigAtomic(*configPath, []byte(cfg), true); err != nil {
		fmt.Fprintln(os.Stderr, "write config:", err)
		os.Exit(1)
	}
	if err := lockACLSystemAdminsOnly(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "lock config file ACL:", err)
		os.Exit(1)
	}

	fmt.Printf("registered host %q (id=%d)\n", *hostname, hostID)
	fmt.Printf("wrote config to %s\n", *configPath)
	fmt.Println("\nnext steps:")
	fmt.Println("  prefer scripts/install-agent-{linux,windows}.{sh,ps1} - they set ACLs, perms, and the hardened service identity.")
	if runtime.GOOS == "windows" {
		fmt.Println("  manual fallback (virtual service account, restricted privileges; no ACL hardening):")
		fmt.Println("    sc.exe create sm-agent binPath= \"\\\"" + selfPath() + "\\\" --config \\\"" + *configPath + "\\\"\" start= auto obj= \"NT SERVICE\\sm-agent\"")
		fmt.Println("    sc.exe privs sm-agent SeChangeNotifyPrivilege/SeImpersonatePrivilege/SeCreateGlobalPrivilege/SeIncreaseWorkingSetPrivilege")
		fmt.Println("    sc.exe sidtype sm-agent unrestricted")
		fmt.Println("    net start sm-agent")
	} else {
		fmt.Println("  manual fallback (assumes a hardened sm-agent.service unit is already installed):")
		fmt.Println("    sudo systemctl enable --now sm-agent")
	}
}

func writeConfigAtomic(path string, data []byte, lockACL bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "agent-*.toml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpName, 0o600); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	if lockACL && runtime.GOOS == "windows" {
		if err := lockACLSystemAdminsOnly(path); err != nil {
			return fmt.Errorf("rewrote %s but failed to relock ACL: %w", path, err)
		}
	}
	return nil
}

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "sm-agent.exe"
	}
	return p
}

func callRegister(server, adminToken, hostname string, intervalS int, insecure bool) (string, int64, string, error) {
	body, _ := json.Marshal(map[string]any{"hostname": hostname, "sample_interval_s": intervalS})
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
		},
	}
	url := strings.TrimRight(server, "/") + "/api/v1/admin/hosts"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", adminToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, "", fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		HostID       int64  `json:"host_id"`
		Token        string `json:"token"`
		ServerPubkey string `json:"server_pubkey"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, "", err
	}
	if out.Token == "" {
		return "", 0, "", errors.New("empty token")
	}
	return out.Token, out.HostID, out.ServerPubkey, nil
}
