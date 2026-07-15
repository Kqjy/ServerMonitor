package upgrade

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"servermonitor/pkg/agentsig"
	"servermonitor/pkg/version"
)

const downloadTimeout = 5 * time.Minute
const maxAgentBinaryBytes int64 = 256 << 20

func Managed() (bool, string) {
	if v, ok := os.LookupEnv("SM_EXTERNALLY_MANAGED"); ok {
		if s := strings.TrimSpace(v); s != "" {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return true, "SM_EXTERNALLY_MANAGED=" + s + " is not a boolean; treating agent as externally managed"
			}
			if b {
				return true, "SM_EXTERNALLY_MANAGED is set"
			}
			return false, ""
		}
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true, "containerized (" + marker + " present)"
		}
	}
	return false, ""
}

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
	if err := writeSignatureAtomic(selfPath+".sig", sigHeader); err != nil {
		opts.Logger.Warn("agent upgraded but could not write the privileged-sync attestation",
			"err", err,
			"path", selfPath+".sig",
			"hint", "the resident agent is current, but the privileged backup copy will need an installer rerun if it reports stale")
	}

	opts.Logger.Info("agent upgrade staged",
		"path", selfPath,
		"bytes", n,
		"hint", "exiting non-zero so the service manager restarts with the new binary")
	return nil
}

// SyncPrivileged promotes a resident agent binary into the root-owned agent
// location used by backup/restore jobs. The resident binary and its signature
// file are deliberately treated as attacker-controlled: the bytes are copied
// to a private staging file, hashed there, and independently verified against
// the installer-pinned server key before the privileged binary is replaced.
func SyncPrivileged(ctx context.Context, sourcePath, signaturePath, pubkeyPath string, logger *slog.Logger) (bool, error) {
	if logger == nil {
		logger = slog.Default()
	}
	selfPath, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate privileged agent: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return false, fmt.Errorf("resolve privileged agent: %w", err)
	}
	if err := verifyDirSafe(filepath.Dir(selfPath)); err != nil {
		return false, err
	}
	if err := verifyRootOwnedPath(filepath.Dir(selfPath)); err != nil {
		return false, fmt.Errorf("privileged agent install path: %w", err)
	}
	if err := verifyPinnedKeyFile(pubkeyPath); err != nil {
		return false, err
	}
	pubkey, err := os.ReadFile(pubkeyPath)
	if err != nil {
		return false, fmt.Errorf("read pinned server pubkey: %w", err)
	}
	sig, err := os.ReadFile(signaturePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read resident upgrade attestation: %w", err)
	}
	if len(sig) > 4096 {
		return false, fmt.Errorf("resident upgrade attestation is unexpectedly large (%d bytes)", len(sig))
	}

	newPath := stagedPath(selfPath)
	_ = os.Remove(newPath)
	digest, n, err := copyStagedBinary(sourcePath, newPath)
	if err != nil {
		_ = os.Remove(newPath)
		return false, err
	}
	if err := agentsig.Verify(strings.TrimSpace(string(pubkey)), strings.TrimSpace(string(sig)), digest); err != nil {
		_ = os.Remove(newPath)
		return false, fmt.Errorf("verify resident agent signature: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(newPath, 0o500); err != nil {
			_ = os.Remove(newPath)
			return false, fmt.Errorf("chmod staged privileged agent: %w", err)
		}
	}
	if err := verifyStagedBinary(newPath, digest); err != nil {
		_ = os.Remove(newPath)
		return false, err
	}
	stagedVersion, err := stagedAgentVersion(ctx, newPath)
	if err != nil {
		_ = os.Remove(newPath)
		return false, err
	}
	if !version.IsNewer(stagedVersion, version.Version) {
		_ = os.Remove(newPath)
		return false, nil
	}
	if err := swap(selfPath, newPath); err != nil {
		_ = os.Remove(newPath)
		return false, err
	}
	logger.Info("privileged backup agent synchronized",
		"from", version.Version,
		"to", stagedVersion,
		"bytes", n,
		"source", sourcePath,
		"target", selfPath)
	return true, nil
}

func copyStagedBinary(sourcePath, stagedPath string) ([]byte, int64, error) {
	src, err := os.Open(sourcePath)
	if err != nil {
		return nil, 0, fmt.Errorf("open resident agent: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(stagedPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		return nil, 0, fmt.Errorf("create staged privileged agent: %w", err)
	}
	digester := agentsig.NewDigest()
	n, copyErr := io.Copy(io.MultiWriter(dst, digester), io.LimitReader(src, maxAgentBinaryBytes+1))
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if copyErr != nil {
		return nil, n, fmt.Errorf("copy resident agent: %w", copyErr)
	}
	if syncErr != nil {
		return nil, n, fmt.Errorf("sync staged privileged agent: %w", syncErr)
	}
	if closeErr != nil {
		return nil, n, fmt.Errorf("close staged privileged agent: %w", closeErr)
	}
	if n > maxAgentBinaryBytes {
		return nil, n, fmt.Errorf("resident agent exceeds %d bytes", maxAgentBinaryBytes)
	}
	if n < 1024 {
		return nil, n, fmt.Errorf("resident agent is suspiciously small (%d bytes)", n)
	}
	return digester.Sum(nil), n, nil
}

func stagedAgentVersion(ctx context.Context, path string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("run staged agent --version: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 || fields[0] != "sm-agent" || fields[1] == "" {
		return "", fmt.Errorf("staged agent returned an invalid version string %q", strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

func stagedPath(selfPath string) string {
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Ext(selfPath), ".exe") {
		return strings.TrimSuffix(selfPath, filepath.Ext(selfPath)) + ".new.exe"
	}
	return selfPath + ".new"
}

func writeSignatureAtomic(path, signature string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(signature)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := replaceFileAtomic(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func verifyPinnedKeyFile(path string) error {
	if err := verifyRootOwnedPath(filepath.Dir(path)); err != nil {
		return fmt.Errorf("pinned server pubkey directory: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat pinned server pubkey: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("pinned server pubkey %q is not a regular file", path)
	}
	if !fileOwnedByRoot(info) {
		return fmt.Errorf("pinned server pubkey %q is not owned by root/SYSTEM", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("pinned server pubkey %q is group- or world-writable (mode %#o)", path, info.Mode().Perm())
	}
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
