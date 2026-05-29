package upgrade

import (
	"bytes"
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

	"servermonitor/pkg/agentsig"
)

const downloadTimeout = 5 * time.Minute
const maxAgentBinaryBytes int64 = 256 << 20

type Options struct {
	ServerURL    string
	Token        string
	ServerPubkey string
	InsecureSkip bool
	Logger       *slog.Logger
}

func Run(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.ServerPubkey) == "" {
		return agentsig.ErrUnknownPubkey
	}

	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}

	installDir := filepath.Dir(selfPath)
	if err := verifyDirSafe(installDir); err != nil {
		return err
	}

	if opts.InsecureSkip {
		opts.Logger.Warn("agent upgrade: TLS certificate verification disabled",
			"hint", "set insecure_skip = false in agent.toml once the server has a trusted cert")
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

	sigHeader := resp.Header.Get(agentsig.SignatureHeader)
	if sigHeader == "" {
		return fmt.Errorf("%w (server did not include %s header)", agentsig.ErrNoSignature, agentsig.SignatureHeader)
	}

	newPath := selfPath + ".new"
	_ = os.Remove(newPath)

	tmp, err := os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("open temp: %w (install dir %q must be writable by the agent)", err, installDir)
	}
	digester := agentsig.NewDigest()
	w := io.MultiWriter(tmp, digester)
	n, copyErr := io.Copy(w, io.LimitReader(resp.Body, maxAgentBinaryBytes+1))
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

	wantDigest := digester.Sum(nil)
	if err := agentsig.Verify(opts.ServerPubkey, sigHeader, wantDigest); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("verify signature: %w", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(newPath, 0o500); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("chmod staged binary: %w", err)
		}
	}

	if err := verifyStagedBinary(newPath, wantDigest); err != nil {
		_ = os.Remove(newPath)
		return err
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

func verifyDirSafe(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat install dir %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("install dir %q is not a directory", dir)
	}
	cur := dir
	for {
		fi, err := os.Stat(cur)
		if err != nil {
			return fmt.Errorf("stat %q: %w", cur, err)
		}
		mode := fi.Mode()
		perm := mode.Perm()
		if mode&os.ModeSticky == 0 {
			if perm&0o002 != 0 {
				return fmt.Errorf("refusing upgrade: %q is world-writable (mode %#o); a writable directory on the install path lets an untrusted user replace the agent binary", cur, perm)
			}
			if perm&0o020 != 0 && !dirGroupOwnedByRunningUID(fi) {
				return fmt.Errorf("refusing upgrade: %q is group-writable (mode %#o) and the group is not the agent's effective uid; tighten with chmod g-w or chown so the group is trusted", cur, perm)
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

func verifyStagedBinary(path string, want []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reopen staged binary: %w", err)
	}
	defer f.Close()
	got, _, err := agentsig.DigestReader(f)
	if err != nil {
		return fmt.Errorf("rehash staged binary: %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("staged binary at %q changed between write and verify (possible tamper)", path)
	}
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
