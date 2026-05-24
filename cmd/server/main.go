package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"servermonitor/internal/server/alerting"
	"servermonitor/internal/server/api"
	"servermonitor/internal/server/archive"
	"servermonitor/internal/server/auth"
	"servermonitor/internal/server/config"
	"servermonitor/internal/server/ingest"
	"servermonitor/internal/server/notify"
	"servermonitor/internal/server/sse"
	"servermonitor/internal/server/storage"
	"servermonitor/internal/server/tasks"
	"servermonitor/internal/server/web"
	"servermonitor/pkg/version"
)

const Version = version.Version

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-password":
			if err := resetPasswordCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "reset-password:", err)
				os.Exit(1)
			}
			return
		case "healthz":
			if err := healthzCmd(); err != nil {
				fmt.Fprintln(os.Stderr, "healthz:", err)
				os.Exit(1)
			}
			return
		case "-h", "--help", "help":
			printUsage()
			return
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := storage.Migrate(cfg.DatabaseURL); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("storage open", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	applied, err := storage.ApplyRetentionPolicies(ctx, db.Pool, storage.RetentionPolicies{
		Raw:           cfg.RetentionRaw,
		Aggregate5m:   cfg.RetentionAggregate5m,
		Processes:     cfg.RetentionProcesses,
		Containers:    cfg.RetentionContainers,
		CompressAfter: cfg.CompressionAfter,
	})
	if err != nil {
		logger.Error("apply retention", "err", err)
		os.Exit(1)
	}
	for _, a := range applied {
		logger.Info("retention policy", "target", a.Target, "kind", a.Kind, "value", a.Value)
	}

	hosts := storage.NewHosts(db)
	batcher := ingest.NewBatcher(db.Pool, cfg.BatcherMaxRows, cfg.BatcherMaxAge, logger)
	batcher.Start(ctx)
	hub := sse.NewHub()
	authSvc := auth.New(db.Pool)

	dispatcher := notify.NewDispatcher(logger)
	alertEngine := alerting.New(db.Pool, dispatcher, hub, logger)
	alertEngine.Start(ctx)

	var archiver *archive.Archiver
	var archiveScheduler *tasks.ArchiveScheduler
	if cfg.S3Bucket != "" {
		archiver, err = archive.New(db.Pool, archive.Config{
			Bucket:       cfg.S3Bucket,
			Region:       cfg.S3Region,
			Prefix:       cfg.S3Prefix,
			UsePathStyle: cfg.S3UsePathStyle,
		}, logger)
		if err != nil {
			logger.Warn("archive disabled", "err", err)
		} else {
			archiveScheduler, err = tasks.NewArchiveScheduler(archiver, logger)
			if err != nil {
				logger.Warn("archive scheduler init", "err", err)
			} else if err := archiveScheduler.Start(ctx); err != nil {
				logger.Warn("archive scheduler start", "err", err)
			}
		}
	}

	go sessionSweeper(ctx, authSvc, logger)

	webHandler, err := web.Handler()
	if err != nil {
		logger.Error("web", "err", err)
		os.Exit(1)
	}

	r := api.New(api.Deps{
		Hosts:          hosts,
		DB:             db,
		Batcher:        batcher,
		Hub:            hub,
		Auth:           authSvc,
		Archive:        archiver,
		Logger:         logger,
		AdminToken:     cfg.AdminToken,
		IngestRate:     cfg.IngestRateLimit,
		IngestBurst:    cfg.IngestBurst,
		Version:        Version,
		WebHandler:     webHandler,
		TrustedProxies: cfg.TrustedProxies,
		SecureCookies:  cfg.SecureBrowserSide(),
		TrustProxyTLS:  cfg.TrustProxyTLS,
		Retention: api.RetentionConfig{
			Raw:               cfg.RetentionRaw,
			Aggregate5m:       cfg.RetentionAggregate5m,
			Processes:         cfg.RetentionProcesses,
			Containers:        cfg.RetentionContainers,
			CompressAfter:     cfg.CompressionAfter,
			RawCutoff:         config.IntervalToDuration(cfg.RetentionRaw),
			Aggregate5mCutoff: config.IntervalToDuration(cfg.RetentionAggregate5m),
		},
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		switch {
		case cfg.ServesTLS():
			logger.Info("server listening (TLS)", "addr", cfg.HTTPAddr, "cert", cfg.TLSCertFile)
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("listen", "err", err)
				cancel()
			}
		default:
			mode := "behind-tls-proxy"
			if cfg.InsecureAllowHTTP {
				mode = "INSECURE_ALLOW_HTTP"
			}
			logger.Info("server listening (plaintext)", "addr", cfg.HTTPAddr, "mode", mode)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("listen", "err", err)
				cancel()
			}
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown", "err", err)
	}
	if err := batcher.Stop(shutdownCtx); err != nil {
		logger.Warn("batcher stop", "err", err)
	}
	alertEngine.Stop()
	if archiveScheduler != nil {
		archiveScheduler.Stop()
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  sm-server                              run the server (env: DATABASE_URL, ADMIN_TOKEN, ...)")
	fmt.Fprintln(os.Stderr, "  sm-server reset-password -username U -password P [-database-url URL]")
	fmt.Fprintln(os.Stderr, "    overwrite a user's password and invalidate all of their sessions.")
	fmt.Fprintln(os.Stderr, "    -username defaults to admin; -password must be supplied; -database-url falls back to $DATABASE_URL.")
	fmt.Fprintln(os.Stderr, "  sm-server healthz")
	fmt.Fprintln(os.Stderr, "    probe /healthz on the local listener and exit 0 on 200; used by Docker HEALTHCHECK.")
}

func healthzCmd() error {
	addr := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("HTTP_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if strings.TrimSpace(os.Getenv("TLS_CERT_FILE")) != "" && strings.TrimSpace(os.Getenv("TLS_KEY_FILE")) != "" {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s/healthz", scheme, net.JoinHostPort(host, port))
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func resetPasswordCmd(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	username := fs.String("username", "admin", "username to reset")
	password := fs.String("password", "", "new password (>=8 chars)")
	dbURL := fs.String("database-url", strings.TrimSpace(os.Getenv("DATABASE_URL")), "PostgreSQL DSN (defaults to $DATABASE_URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *password == "" {
		return errors.New("-password is required")
	}
	if *dbURL == "" {
		return errors.New("DATABASE_URL not set and -database-url not provided")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := storage.Open(ctx, *dbURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := auth.New(db.Pool).ResetPassword(ctx, *username, *password); err != nil {
		if errors.Is(err, auth.ErrNoUser) {
			return fmt.Errorf("no user named %q", *username)
		}
		return err
	}
	fmt.Printf("password reset for user %q; all existing sessions invalidated\n", *username)
	return nil
}

func sessionSweeper(ctx context.Context, svc *auth.Service, logger *slog.Logger) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := svc.PurgeExpired(ctx); err != nil {
				logger.Warn("session sweep", "err", err)
			}
		}
	}
}
