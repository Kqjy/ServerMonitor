package upgrade

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const downloadTimeout = 5 * time.Minute
const maxAgentBinaryBytes int64 = 256 << 20

type Options struct {
	ServerURL    string
	Token        string
	InsecureSkip bool
	Logger       *slog.Logger
}

func Run(ctx context.Context, opts Options) error {
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH

	dlCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	url := strings.TrimRight(opts.ServerURL, "/") + "/api/v1/agent/binary?platform=" + platform
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Agent-Token", opts.Token)
	req.Header.Set("User-Agent", "sm-agent-upgrade/"+platform)

	httpClient := &http.Client{
		Timeout: downloadTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.InsecureSkip},
		},
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	dir := filepath.Dir(selfPath)
	newPath := selfPath + ".new"
	_ = os.Remove(newPath)

	tmp, err := os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("open temp: %w (install dir %q must be writable by the agent)", err, dir)
	}
	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxAgentBinaryBytes+1))
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("write temp: %w", copyErr)
	}
	if syncErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("sync temp: %w", syncErr)
	}
	if closeErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("close temp: %w", closeErr)
	}
	if n > maxAgentBinaryBytes {
		_ = os.Remove(newPath)
		return fmt.Errorf("downloaded binary exceeds %d bytes (refusing to install)", maxAgentBinaryBytes)
	}
	if n < 1024 {
		_ = os.Remove(newPath)
		return fmt.Errorf("downloaded binary suspiciously small (%d bytes)", n)
	}

	if err := swap(selfPath, newPath); err != nil {
		_ = os.Remove(newPath)
		return err
	}

	opts.Logger.Info("agent upgrade staged",
		"path", selfPath,
		"bytes", n,
		"hint", "exiting non-zero so the service manager restarts with the new binary")
	return nil
}

func swap(selfPath, newPath string) error {
	if runtime.GOOS == "windows" {
		oldPath := selfPath + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(selfPath, oldPath); err != nil {
			return fmt.Errorf("rename running exe: %w", err)
		}
		if err := os.Rename(newPath, selfPath); err != nil {
			_ = os.Rename(oldPath, selfPath)
			return fmt.Errorf("install new exe: %w", err)
		}
		return nil
	}
	if err := os.Rename(newPath, selfPath); err != nil {
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}
