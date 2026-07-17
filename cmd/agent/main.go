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
	"text/tabwriter"
	"time"

	agentbackup "servermonitor/internal/agent/backup"
	"servermonitor/internal/agent/backupnode"
	"servermonitor/internal/agent/backupsched"
	"servermonitor/internal/agent/collectors"
	"servermonitor/internal/agent/config"
	"servermonitor/internal/agent/runner"
	"servermonitor/internal/agent/spool"
	"servermonitor/internal/agent/transport"
	"servermonitor/internal/agent/upgrade"
	"servermonitor/pkg/version"
	"servermonitor/pkg/wire"
)

const backupBrowseJobTimeout = 5 * time.Minute

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "register":
			registerCmd(os.Args[2:])
			return
		case "sync-privileged":
			syncPrivilegedCmd(os.Args[2:])
			return
		case "backup":
			backupCmd(os.Args[2:])
			return
		case "healthz":
			if err := healthzCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "healthz:", err)
				if errors.Is(err, errHealthMissing) {
					os.Exit(2)
				}
				os.Exit(1)
			}
			return
		}
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
	if cfg.HealthPath == "" {
		cfg.HealthPath = defaultHealthPath()
	}
	client.SetHealthPath(cfg.HealthPath)
	r := runner.New(cfg, client, logger)

	managed, managedReason := upgrade.Managed()
	r.SetExternallyManaged(managed)
	if managed {
		logger.Info("agent self-upgrade disabled: externally managed",
			"reason", managedReason,
			"hint", "update by redeploying a new agent image tag (bump SM_AGENT_IMAGE, then docker compose up -d --pull always --no-build)")
	}

	pk := &pubkeyHolder{v: cfg.ServerPubkey}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client.StartDrainer(ctx)
	go runBackupBrowseLoop(ctx, client, logger)

	nodeManager := backupnode.New(cfg.ServerURL, cfg.Token, cfg.InsecureSkip, filepath.Dir(cfg.BackupStatusPath), logger)
	go nodeManager.Run(ctx)

	backupScheduler := maybeStartBackupScheduler(ctx, logger)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-client.IntervalUpdates():
				if !ok {
					return
				}
				switch err := persistInterval(*configPath, s); {
				case err == nil:
					logger.Info("interval updated from server", "interval_s", s)
				case errors.Is(err, errNoConfigFile):
					logger.Debug("interval applied in memory; running with env-var identity, nothing to persist", "interval_s", s)
				default:
					logger.Warn("persist interval failed", "err", err)
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
			warnedManaged bool
		)
		for {
			select {
			case <-ctx.Done():
				return
			case upd, ok := <-client.ControlUpdates():
				if !ok {
					return
				}
				if upd.ServerPubkey != "" && !managed && pk.setIfEmpty(upd.ServerPubkey) {
					switch err := persistServerPubkey(*configPath, upd.ServerPubkey); {
					case err == nil:
						logger.Info("pinned server pubkey from ingest ack",
							"pubkey", upd.ServerPubkey,
							"config", *configPath)
					case errors.Is(err, errNoConfigFile):
						logger.Info("pinned server pubkey in memory; running with env-var identity, nothing to persist",
							"config", *configPath)
					default:
						logger.Warn("pinned server pubkey in memory but failed to persist to agent.toml; will retry after next restart",
							"err", err,
							"config", *configPath)
					}
				}
				latest := upd.LatestVersion
				if latest == "" || !version.IsNewer(latest, runner.Version) {
					continue
				}
				if managed {
					if !warnedManaged {
						logger.Info("agent upgrade available but this agent is externally managed; not self-upgrading",
							"current", runner.Version,
							"latest", latest,
							"reason", managedReason,
							"hint", "rebuild & push a new image tag, bump SM_AGENT_IMAGE, then redeploy (docker compose up -d --pull always --no-build)")
						warnedManaged = true
					}
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

	runErr := r.Run(ctx)
	if backupScheduler != nil {
		cancel()
		waitScheduler(backupScheduler, 40*time.Second)
	}
	if runErr != nil {
		if errors.Is(runErr, runner.ErrDeregistered) {
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
		logger.Error("runner", "err", runErr)
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

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func maybeStartBackupScheduler(ctx context.Context, logger *slog.Logger) *backupsched.Scheduler {
	if !agentbackup.IsContainerized() || !backupEnabledEnv(os.Getenv) {
		return nil
	}
	res, err := agentbackup.Provision(os.Getenv)
	if err != nil {
		logger.Error("backup provisioning failed; scheduled backups disabled this boot", "err", err)
		return nil
	}
	if res.KeyGenerated {
		logger.Info("generated backup encryption key (kept forever; export it with: sm-agent backup recovery-kit)")
	}
	configPath := res.ConfigPath
	enrollOpts := agentbackup.EnrollOptions{
		ConfigPath: configPath,
		ServerURL:  os.Getenv("SM_SERVER_URL"),
		Token:      os.Getenv("SM_TOKEN"),
		KeyPath:    filepath.Join(filepath.Dir(configPath), "tunnel.key"),
	}
	if res.TunnelRepos {
		if err := agentbackup.EnsureTunnelEnrolled(ctx, configPath, enrollOpts); err != nil {
			logger.Error("automatic backup tunnel enrollment failed; scheduled runs will retry", "err", err)
		}
	}
	cfg, err := agentbackup.LoadForScheduling(configPath)
	if err != nil {
		logger.Error("could not load backup config after provisioning; scheduled backups disabled", "err", err)
		return nil
	}
	if cfg.Schedule == nil || !cfg.Schedule.Enabled {
		logger.Info("backup provisioned but scheduling is off; run backups manually with: sm-agent backup run")
		return nil
	}
	sched := backupsched.New(schedulerConfig(cfg.Schedule), backupsched.Options{
		StatePath: filepath.Join(filepath.Dir(cfg.StatusPath), "backup-schedule.json"),
		RunBackup: func(ctx context.Context) error {
			if cfg.HasTunnelRepos() {
				if err := agentbackup.EnsureTunnelEnrolled(ctx, configPath, enrollOpts); err != nil {
					if statusErr := agentbackup.RecordTunnelEnrollmentFailure(configPath, err, time.Now()); statusErr != nil {
						logger.Warn("could not record tunnel enrollment failure in backup status", "err", statusErr)
					}
					return fmt.Errorf("tunnel enrollment failed: %w", err)
				}
			}
			if _, err := agentbackup.InitFromConfigPath(ctx, configPath, agentbackup.BaseOptions(logger)); err != nil {
				return err
			}
			_, err := agentbackup.RunFromConfigPath(ctx, configPath, agentbackup.BaseOptions(logger))
			return err
		},
		RunCheck: func(ctx context.Context) error {
			if cfg.HasTunnelRepos() {
				if err := agentbackup.EnsureTunnelEnrolled(ctx, configPath, enrollOpts); err != nil {
					return fmt.Errorf("tunnel enrollment failed: %w", err)
				}
			}
			_, err := agentbackup.CheckFromConfigPath(ctx, configPath, agentbackup.CheckOptions{ReadDataSubset: cfg.Schedule.CheckReadDataSubset}, agentbackup.BaseOptions(logger))
			return err
		},
		Logger: logger.With("component", "backupsched"),
	})
	collectors.SetBackupNextRunSource(func(context.Context) *time.Time { return sched.NextBackupRun() })
	collectors.SetBackupScheduledRepos(repoNames(cfg.Repos))
	go sched.Run(ctx)
	logger.Info("scheduled backups enabled", "config", configPath, "backup_time", cfg.Schedule.BackupTime)
	return sched
}

func repoNames(repos []agentbackup.Repo) []string {
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repo.Name)
	}
	return out
}

func backupEnabledEnv(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("SM_ENABLE_BACKUP"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func schedulerConfig(s *agentbackup.ScheduleSettings) backupsched.Config {
	backupTime := s.BackupTime
	if backupTime == "" {
		backupTime = "02:30"
	}
	checkTime := s.CheckTime
	if checkTime == "" {
		checkTime = "00:00"
	}
	backupJitter := 900
	if s.BackupJitterS > 0 {
		backupJitter = s.BackupJitterS
	}
	checkJitter := 21600
	if s.CheckJitterS > 0 {
		checkJitter = s.CheckJitterS
	}
	return backupsched.Config{
		BackupTime:   backupTime,
		BackupJitter: time.Duration(backupJitter) * time.Second,
		CheckWeekday: parseWeekday(s.CheckWeekday),
		CheckTime:    checkTime,
		CheckJitter:  time.Duration(checkJitter) * time.Second,
	}
}

func parseWeekday(s string) time.Weekday {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sunday":
		return time.Sunday
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	default:
		return time.Monday
	}
}

func waitScheduler(s *backupsched.Scheduler, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func syncPrivilegedCmd(args []string) {
	fs := flag.NewFlagSet("sync-privileged", flag.ExitOnError)
	source := fs.String("source", "", "resident self-updating agent binary")
	signature := fs.String("signature", "", "server signature attestation for the resident binary")
	pubkeyFile := fs.String("pubkey-file", "", "installer-pinned server Ed25519 public key")
	_ = fs.Parse(args)
	if *source == "" || *signature == "" || *pubkeyFile == "" {
		fmt.Fprintln(os.Stderr, "usage: sm-agent sync-privileged --source PATH --signature PATH --pubkey-file PATH")
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	updated, err := upgrade.SyncPrivileged(context.Background(), *source, *signature, *pubkeyFile, logger)
	if err != nil {
		logger.Error("privileged backup agent synchronization failed", "err", err)
		os.Exit(1)
	}
	if !updated {
		logger.Debug("privileged backup agent already current or no signed resident update is staged")
	}
}

func backupCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sm-agent backup {run|init|snapshots|ls|check|restore|tunnel-enroll|proxy|provision|recovery-kit} [options]")
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch args[0] {
	case "run":
		fs := flag.NewFlagSet("backup run", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		_ = fs.Parse(args[1:])
		result, err := agentbackup.RunFromConfigPath(ctx, *configPath, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup run failed", "err", err)
			os.Exit(1)
		}
		if !result.AnySucceeded() {
			os.Exit(1)
		}
	case "init":
		fs := flag.NewFlagSet("backup init", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		_ = fs.Parse(args[1:])
		result, err := agentbackup.InitFromConfigPath(ctx, *configPath, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup init failed", "err", err)
			os.Exit(1)
		}
		if result.Failed() {
			os.Exit(1)
		}
	case "snapshots":
		fs := flag.NewFlagSet("backup snapshots", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		repoName := fs.String("repo", "", "repo name")
		jsonOut := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		result, err := agentbackup.SnapshotsFromConfigPath(ctx, *configPath, *repoName, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup snapshots failed", "err", err)
			os.Exit(1)
		}
		for _, repo := range result.Repos {
			if repo.Error != "" {
				logger.Error("backup snapshots failed", "repo", repo.Name, "err", repo.Error)
			}
		}
		if *jsonOut {
			err = result.WriteJSON(os.Stdout, *repoName != "")
		} else {
			err = result.WriteTable(os.Stdout)
		}
		if err != nil {
			logger.Error("write snapshots output failed", "err", err)
			os.Exit(1)
		}
		if result.Failed() {
			os.Exit(1)
		}
	case "ls":
		fs := flag.NewFlagSet("backup ls", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		repoName := fs.String("repo", "", "repo name")
		snapshot := fs.String("snapshot", "latest", "snapshot id or latest")
		recursive := fs.Bool("recursive", false, "list recursively")
		jsonOut := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		if fs.NArg() > 1 {
			fmt.Fprintln(os.Stderr, "usage: sm-agent backup ls [options] [path]")
			os.Exit(2)
		}
		path := "/"
		if fs.NArg() == 1 {
			path = fs.Arg(0)
		}
		cfg, err := agentbackup.Load(*configPath)
		if err != nil {
			logger.Error("backup ls failed", "err", err)
			os.Exit(1)
		}
		if *repoName == "" {
			if len(cfg.Repos) != 1 {
				logger.Error("backup ls failed", "err", "--repo is required when more than one repository is configured")
				os.Exit(2)
			}
			*repoName = cfg.Repos[0].Name
		}
		result, err := agentbackup.BrowseFromConfigPath(ctx, *configPath, agentbackup.BrowseOptions{
			Repo:       *repoName,
			Snapshot:   *snapshot,
			Path:       path,
			Recursive:  *recursive,
			MaxEntries: 100000,
		}, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup ls failed", "err", err)
			os.Exit(1)
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(result.Entries); err != nil {
				logger.Error("write backup ls output failed", "err", err)
				os.Exit(1)
			}
		} else {
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tSIZE\tMODIFIED")
			for _, entry := range result.Entries {
				name := entry.Name
				size := ""
				modified := ""
				if entry.Type == "dir" {
					name += "/"
				} else if entry.Type == "file" {
					size = fmt.Sprintf("%d", entry.Size)
				}
				if entry.Mtime != nil {
					modified = entry.Mtime.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, entry.Type, size, modified)
			}
			if err := tw.Flush(); err != nil {
				logger.Error("write backup ls output failed", "err", err)
				os.Exit(1)
			}
		}
		if result.Truncated {
			fmt.Fprintln(os.Stderr, "listing truncated at 100000 entries")
		}
	case "check":
		fs := flag.NewFlagSet("backup check", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		repoName := fs.String("repo", "", "restrict the check to one repo by name")
		readDataSubset := fs.String("read-data-subset", "", "verify a subset of pack data (e.g. 5%, 250M, 1/5)")
		drill := fs.Bool("drill", false, "after a successful check, restore a small file sample and byte-compare it against the live files")
		_ = fs.Parse(args[1:])
		result, err := agentbackup.CheckFromConfigPath(ctx, *configPath, agentbackup.CheckOptions{
			Repo:           *repoName,
			ReadDataSubset: *readDataSubset,
			Drill:          *drill,
		}, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup check failed", "err", err)
			os.Exit(1)
		}
		if !result.AllSucceeded() {
			os.Exit(1)
		}
	case "tunnel-enroll":
		fs := flag.NewFlagSet("backup tunnel-enroll", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		agentConfigPath := fs.String("agent-config", defaultConfigPath(), "path to agent.toml (server url + token)")
		keyPath := fs.String("key", "", "path to the wireguard private key file (default: tunnel.key next to backup.toml)")
		_ = fs.Parse(args[1:])
		agentCfg, err := config.Load(*agentConfigPath)
		if err != nil {
			logger.Error("tunnel enroll failed", "err", err)
			os.Exit(1)
		}
		result, err := agentbackup.TunnelEnroll(ctx, agentbackup.EnrollOptions{
			ConfigPath:   *configPath,
			ServerURL:    agentCfg.ServerURL,
			Token:        agentCfg.Token,
			InsecureSkip: agentCfg.InsecureSkip,
			KeyPath:      *keyPath,
		})
		if err != nil {
			logger.Error("tunnel enroll failed", "err", err)
			os.Exit(1)
		}
		if result.KeyGenerated {
			fmt.Printf("generated wireguard key: %s\n", result.KeyPath)
		} else {
			fmt.Printf("kept existing wireguard key: %s\n", result.KeyPath)
		}
		fmt.Printf("enrolled: tunnel ip %s -> server %s (endpoint %s)\n", result.TunnelIP, result.ServerTunnelIP, result.Endpoint)
		fmt.Printf("server public key: %s\n", result.ServerPublicKey)
		for _, node := range result.Nodes {
			fmt.Printf("backup node %s: %s (endpoint %s)\n", node.Host, node.IP, node.Endpoint)
		}
		if result.Warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", result.Warning)
		}
	case "provision":
		fs := flag.NewFlagSet("backup provision", flag.ExitOnError)
		_ = fs.Parse(args[1:])
		res, err := agentbackup.Provision(os.Getenv)
		if err != nil {
			logger.Error("backup provision failed", "err", err)
			os.Exit(1)
		}
		fmt.Printf("provisioned %s (key_generated=%v, config_rewritten=%v)\n", res.ConfigPath, res.KeyGenerated, res.Rewrote)
	case "recovery-kit":
		fs := flag.NewFlagSet("backup recovery-kit", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		_ = fs.Parse(args[1:])
		kit, err := agentbackup.RenderRecoveryKit(*configPath)
		if err != nil {
			logger.Error("backup recovery-kit failed", "err", err)
			os.Exit(1)
		}
		fmt.Print(kit)
	case "proxy":
		fs := flag.NewFlagSet("backup proxy", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		listen := fs.String("listen", "127.0.0.1:0", "loopback address for the local restic proxy")
		_ = fs.Parse(args[1:])
		cfg, err := agentbackup.Load(*configPath)
		if err != nil {
			logger.Error("backup proxy failed", "err", err)
			os.Exit(1)
		}
		info, err := agentbackup.StartProxy(cfg, *listen, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup proxy failed", "err", err)
			os.Exit(1)
		}
		defer info.Release()
		defer info.Session.Close()
		if err := info.Session.Handshaken(15 * time.Second); err != nil {
			logger.Error("backup proxy failed", "err", err)
			info.Session.Close()
			info.Release()
			os.Exit(1)
		}
		fmt.Println("tunnel up; local restic proxies:")
		for _, endpoint := range info.Endpoints {
			fmt.Printf("  repo %s (via %s):\n    export RESTIC_REPOSITORY=%s\n", endpoint.RepoName, endpoint.Destination, endpoint.RepoURL)
		}
		fmt.Println("press Ctrl+C to stop")
		<-ctx.Done()
	case "restore":
		fs := flag.NewFlagSet("backup restore", flag.ExitOnError)
		configPath := fs.String("config", agentbackup.DefaultConfigPath(), "path to backup.toml")
		repoName := fs.String("repo", "", "repo name to restore from")
		snapshot := fs.String("snapshot", "", "snapshot id to restore")
		target := fs.String("target", "", "restore into this directory (must be inside the restore root)")
		inPlace := fs.Bool("in-place", false, "restore over the live files at their original locations")
		instance := fs.String("instance", "", "repo:snapshot[:include,include] identity for the sanctioned in-place restore unit")
		var includes stringList
		fs.Var(&includes, "include", "restore only this path (repeatable)")
		_ = fs.Parse(args[1:])
		result, err := agentbackup.RestoreFromConfigPath(ctx, *configPath, agentbackup.RestoreOptions{
			Repo:     *repoName,
			Snapshot: *snapshot,
			Includes: includes,
			Target:   *target,
			InPlace:  *inPlace,
			Instance: *instance,
		}, agentbackup.BaseOptions(logger))
		if err != nil {
			logger.Error("backup restore failed", "err", err)
			os.Exit(1)
		}
		if result.LockSkipped {
			break
		}
		if result.InPlace {
			fmt.Printf("restored in place over live files\n")
		} else {
			fmt.Printf("restored to %s\n", result.Target)
		}
		if result.Summary != nil {
			fmt.Printf("files restored: %d/%d  bytes restored: %d/%d  skipped: %d\n",
				result.Summary.FilesRestored, result.Summary.TotalFiles,
				result.Summary.BytesRestored, result.Summary.TotalBytes, result.Summary.FilesSkipped)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: sm-agent backup {run|init|snapshots|ls|check|restore|tunnel-enroll|proxy|provision|recovery-kit} [options]")
		os.Exit(2)
	}
}

func runBackupBrowseLoop(ctx context.Context, client *transport.Client, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-client.BrowseSignals():
			if !ok {
				return
			}
		}
		idleDeadline := time.Now().Add(60 * time.Second)
		for {
			jobs, err := client.FetchBackupBrowseJobs(ctx)
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, transport.ErrDeregistered) {
					return
				}
				logger.Warn("fetch backup browse jobs failed", "err", err)
			} else {
				for _, job := range jobs {
					jobCtx, cancel := context.WithTimeout(ctx, backupBrowseJobTimeout)
					result, browseErr := agentbackup.BrowseFromConfigPath(jobCtx, agentbackup.DefaultConfigPath(), agentbackup.BrowseOptions{
						Repo:       job.Repo,
						Snapshot:   job.Snapshot,
						Path:       job.Path,
						MaxEntries: 2000,
					}, agentbackup.BaseOptions(logger))
					cancel()
					response := wire.BackupBrowseResult{}
					if browseErr != nil {
						kind, message := agentbackup.ClassifyBrowseError(browseErr)
						if kind == "insufficient_privilege" {
							message += "; fallback command: " + backupBrowseFallbackCommand(job, runtime.GOOS)
						}
						response.ErrorKind = kind
						response.Error = message
					} else {
						response.Entries = result.Entries
						response.Truncated = result.Truncated
					}
					if err := client.PostBackupBrowseResult(ctx, job.ID, response); err != nil {
						if ctx.Err() != nil || errors.Is(err, transport.ErrDeregistered) {
							return
						}
						logger.Warn("post backup browse result failed", "job", job.ID, "err", err)
					}
					idleDeadline = time.Now().Add(60 * time.Second)
				}
			}
			if len(jobs) == 0 && time.Now().After(idleDeadline) {
				break
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func backupBrowseFallbackCommand(job wire.BackupBrowseJob, goos string) string {
	if goos == "windows" {
		return "& 'C:\\Program Files\\ServerMonitor\\sm-agent.exe' backup ls --repo " + powershellSingleQuote(job.Repo) + " --snapshot " + powershellSingleQuote(job.Snapshot) + " " + powershellSingleQuote(job.Path)
	}
	return "sudo /usr/local/bin/sm-agent backup ls --repo " + shellSingleQuote(job.Repo) + " --snapshot " + shellSingleQuote(job.Snapshot) + " " + shellSingleQuote(job.Path)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func powershellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

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

var errNoConfigFile = errors.New("no on-disk config to persist into (env-var identity)")

func persistConfigField(path, key, replacement string) error {
	configFileMu.Lock()
	defer configFileMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return errNoConfigFile
		}
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

func defaultHealthPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "health.json")
		}
		return `C:\ProgramData\ServerMonitor\health.json`
	}
	return "/var/lib/servermonitor/health.json"
}

var errHealthMissing = errors.New("no health record yet (agent has not completed a successful push)")

func healthzCmd(args []string) error {
	fs := flag.NewFlagSet("healthz", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to agent.toml")
	maxAge := fs.Duration("max-age", 0, "maximum age of the last successful push (default: 3×interval_s, minimum 60s)")
	pathOverride := fs.String("path", "", "path to health.json (overrides config + default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxAge < 0 {
		return fmt.Errorf("--max-age must be >= 0 (got %s)", *maxAge)
	}

	var configExplicit, maxAgeExplicit bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config":
			configExplicit = true
		case "max-age":
			maxAgeExplicit = true
		}
	})

	healthPath := *pathOverride
	intervalS := 0
	if healthPath == "" {
		cfg, err := config.Load(*configPath)
		if err == nil {
			healthPath = cfg.HealthPath
			intervalS = cfg.IntervalS
		} else if configExplicit || !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: failed to load config %s: %v\n", *configPath, err)
		}
	}
	if healthPath == "" {
		healthPath = defaultHealthPath()
	}

	data, err := os.ReadFile(healthPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s (%v)", errHealthMissing, healthPath, err)
		}
		return fmt.Errorf("read %s: %w", healthPath, err)
	}
	var rec struct {
		LastPushAt time.Time `json:"last_push_at"`
		IntervalS  int       `json:"interval_s"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("%w: %s parse failed: %v", errHealthMissing, healthPath, err)
	}
	if rec.LastPushAt.IsZero() {
		return fmt.Errorf("%w: %s missing last_push_at", errHealthMissing, healthPath)
	}

	threshold := *maxAge
	if !maxAgeExplicit {
		eff := rec.IntervalS
		if eff <= 0 {
			eff = intervalS
		}
		if eff <= 0 {
			eff = 10
		}
		threshold = time.Duration(eff*3) * time.Second
		if threshold < 60*time.Second {
			threshold = 60 * time.Second
		}
	}

	age := time.Since(rec.LastPushAt)
	if age > threshold {
		return fmt.Errorf("last push %s ago, exceeds max-age %s", age.Truncate(time.Second), threshold)
	}
	fmt.Printf("ok: last push %s ago (max-age %s)\n", age.Truncate(time.Second), threshold)
	return nil
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
