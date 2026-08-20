package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var DefaultLinuxExcludes = []string{
	"**/.cache",
	"/var/lib/servermonitor",
	"/var/lib/docker/overlay2",
	"/var/lib/docker/overlay",
	"/var/lib/docker/image",
	"/var/lib/docker/containers",
	"/var/lib/docker/buildkit",
	"/var/lib/docker/tmp",
	"/var/lib/docker/plugins",
	"/var/lib/docker/network",
	"/var/lib/docker/runtimes",
	"/var/lib/docker/aufs",
	"/var/lib/docker/btrfs",
	"/var/lib/docker/devicemapper",
	"/var/lib/docker/fuse-overlayfs",
	"/var/lib/docker/vfs",
	"/var/lib/docker/zfs",
	"/var/lib/containerd",
}

const dockerVolumesPath = "/var/lib/docker/volumes"

type ProvisionResult struct {
	ConfigPath   string
	KeyGenerated bool
	Rewrote      bool
	TunnelRepos  bool
}

func Provision(getenv func(string) string) (ProvisionResult, error) {
	configPath := strings.TrimSpace(getenv("SM_BACKUP_CONFIG"))
	if configPath == "" {
		configPath = DefaultConfigPath()
	}
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ProvisionResult{}, fmt.Errorf("create backup config dir: %w", err)
	}
	res := ProvisionResult{ConfigPath: configPath}

	keyPath := filepath.Join(dir, "backup.key")
	generated, err := ensureBackupKey(keyPath)
	if err != nil {
		return res, err
	}
	res.KeyGenerated = generated

	if err := stageRestic(getenv, dir); err != nil {
		return res, err
	}

	repos := splitCSV(getenv("SM_BACKUP_REPOS"))
	if len(repos) == 0 {
		if _, statErr := os.Stat(configPath); statErr == nil {
			needServer, nodes, demandErr := tunnelRepoDemand(configPath)
			if demandErr == nil {
				res.TunnelRepos = needServer || len(nodes) > 0
			}
			return res, nil
		}
		return res, fmt.Errorf("SM_BACKUP_REPOS is required to provision backups, or provide an operator-managed backup.toml at %s", configPath)
	}

	credsPath := filepath.Join(dir, "repo-credentials.env")
	haveCreds, err := writeCredsFile(getenv, credsPath)
	if err != nil {
		return res, err
	}

	toml, err := renderBackupTOML(getenv, keyPath, credsPath, haveCreds, func(path string) error {
		_, statErr := os.Stat(path)
		return statErr
	})
	if err != nil {
		return res, err
	}
	if err := writeFileAtomic(configPath, []byte(toml), 0o600); err != nil {
		return res, err
	}
	res.Rewrote = true
	for _, repo := range repos {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(repo)), "tunnel:") {
			res.TunnelRepos = true
			break
		}
	}
	return res, nil
}

func ensureBackupKey(path string) (bool, error) {
	if info, err := os.Stat(path); err == nil {
		if info.Size() > 0 {
			return false, nil
		}
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("remove truncated backup key %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat backup key: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return false, err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("create backup key: %w", err)
	}
	if _, err := file.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		file.Close()
		os.Remove(path)
		return false, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return false, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return false, err
	}
	return true, nil
}

func writeCredsFile(getenv func(string) string, path string) (bool, error) {
	pairs := [][2]string{
		{"AWS_ACCESS_KEY_ID", getenv("SM_BACKUP_S3_ACCESS_KEY_ID")},
		{"AWS_SECRET_ACCESS_KEY", getenv("SM_BACKUP_S3_SECRET_ACCESS_KEY")},
		{"AWS_SESSION_TOKEN", getenv("SM_BACKUP_S3_SESSION_TOKEN")},
		{"RESTIC_REST_USERNAME", getenv("SM_BACKUP_REST_USERNAME")},
		{"RESTIC_REST_PASSWORD", getenv("SM_BACKUP_REST_PASSWORD")},
	}
	var b strings.Builder
	have := false
	for _, pair := range pairs {
		value := strings.TrimSpace(pair[1])
		if value == "" {
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", pair[0], value)
		have = true
	}
	if !have {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(b.String()), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func renderBackupTOML(getenv func(string) string, keyPath, credsPath string, haveCreds bool, stat func(string) error) (string, error) {
	repos := splitCSV(getenv("SM_BACKUP_REPOS"))
	names := splitCSV(getenv("SM_BACKUP_REPO_NAMES"))
	pathsValue := strings.TrimSpace(getenv("SM_BACKUP_PATHS"))
	paths := splitCSV(pathsValue)
	if pathsValue == "" {
		paths = []string{"/etc", "/home", "/root", "/var/lib"}
		if stat(HostRootFromEnv(getenv)+dockerVolumesPath) == nil {
			paths = append(paths, dockerVolumesPath)
		}
	}
	excludes := resolveExcludes(getenv)
	oneFileSystem := true
	if value := strings.TrimSpace(getenv("SM_BACKUP_ONE_FILE_SYSTEM")); value != "" {
		if parsed, parseErr := strconv.ParseBool(value); parseErr == nil {
			oneFileSystem = parsed
		}
	}
	pruneMode := strings.TrimSpace(getenv("SM_BACKUP_PRUNE_MODE"))
	if pruneMode == "" {
		pruneMode = "external"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "status_path = %q\n", DefaultStatusPath())
	fmt.Fprintf(&b, "restic_path = %q\n", stagedResticPath(filepath.Dir(keyPath)))
	b.WriteString("paths = [")
	for i, path := range paths {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", path)
	}
	b.WriteString("]\n")
	b.WriteString("excludes = [")
	for i, exclude := range excludes {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", exclude)
	}
	b.WriteString("]\n")
	fmt.Fprintf(&b, "one_file_system = %t\n", oneFileSystem)
	fmt.Fprintf(&b, "prune_mode = %q\n", pruneMode)
	writeScheduleTOML(&b, getenv)
	b.WriteString("\n[retention]\ndaily = 7\nweekly = 4\nmonthly = 6\n")

	for i, url := range repos {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		tunnelNode := ""
		tunnelName := ""
		if strings.HasPrefix(strings.ToLower(url), "tunnel:") {
			var err error
			tunnelNode, tunnelName, err = parseTunnelRepository(url)
			if err != nil {
				return "", err
			}
		} else if !hasRemoteScheme(url) {
			return "", fmt.Errorf("repository %q is not a supported backend for the containerized agent; use rest:/s3:/b2:/gs:/azure:/swift: or tunnel:NAME/tunnel:NODE/NAME. sftp:/rclone: need helper binaries absent from the image, and a local path would resolve to the host during backup but the container during snapshots", url)
		}
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		if name == "" {
			if tunnelName != "" {
				name = tunnelName
			} else {
				name = fmt.Sprintf("repo%d", i+1)
			}
		}
		b.WriteString("\n[[repo]]\n")
		fmt.Fprintf(&b, "name = %q\n", name)
		if tunnelName != "" {
			fmt.Fprintf(&b, "tunnel_name = %q\n", tunnelName)
			if tunnelNode != "" {
				fmt.Fprintf(&b, "tunnel_node = %q\n", tunnelNode)
			}
		} else {
			fmt.Fprintf(&b, "url = %q\n", url)
		}
		fmt.Fprintf(&b, "password_file = %q\n", keyPath)
		if haveCreds && (tunnelName != "" || isCredentialRepo(url)) {
			fmt.Fprintf(&b, "env_file = %q\n", credsPath)
		}
		if strings.HasPrefix(url, "s3:") {
			if region := strings.TrimSpace(getenv("SM_BACKUP_S3_REGION")); region != "" {
				fmt.Fprintf(&b, "s3_region = %q\n", region)
			}
			if strings.TrimSpace(getenv("SM_BACKUP_S3_PATH_STYLE")) == "1" {
				b.WriteString("s3_path_style = true\n")
			}
		}
	}
	return b.String(), nil
}

func resolveExcludes(getenv func(string) string) []string {
	value := strings.TrimSpace(getenv("SM_BACKUP_EXCLUDES"))
	if value == "" {
		return append([]string(nil), DefaultLinuxExcludes...)
	}
	if strings.EqualFold(value, "none") {
		return []string{}
	}
	if strings.HasPrefix(value, "+") {
		return append(append([]string(nil), DefaultLinuxExcludes...), splitCSV(value[1:])...)
	}
	return splitCSV(value)
}

func parseTunnelRepository(spec string) (string, string, error) {
	remainder := strings.TrimSpace(spec[len("tunnel:"):])
	if remainder == "" {
		return "", "", fmt.Errorf("tunnel repository %q is missing a repository name; use tunnel:NAME or tunnel:NODE/NAME", spec)
	}
	if strings.Count(remainder, "/") > 1 {
		return "", "", fmt.Errorf("tunnel repository %q has too many path separators; use tunnel:NAME or tunnel:NODE/NAME", spec)
	}
	node := ""
	name := remainder
	if strings.Contains(remainder, "/") {
		node, name, _ = strings.Cut(remainder, "/")
		if node == "" {
			return "", "", fmt.Errorf("tunnel repository %q is missing the node name before /; use tunnel:NODE/NAME", spec)
		}
		if name == "" {
			return "", "", fmt.Errorf("tunnel repository %q is missing the repository name after /; use tunnel:NODE/NAME", spec)
		}
	}
	if !tunnelRepoNameRE.MatchString(name) {
		return "", "", fmt.Errorf("tunnel repository %q has an invalid repository name %q; it must match %s", spec, name, tunnelRepoNameRE)
	}
	if node != "" && !tunnelRepoNameRE.MatchString(node) {
		return "", "", fmt.Errorf("tunnel repository %q has an invalid node name %q; it must match %s", spec, node, tunnelRepoNameRE)
	}
	return node, name, nil
}

func writeScheduleTOML(b *strings.Builder, getenv func(string) string) {
	if !scheduleEnabled(getenv) {
		return
	}
	b.WriteString("\n[schedule]\nenabled = true\n")
	fmt.Fprintf(b, "backup_time = %q\n", envOr(getenv, "SM_BACKUP_TIME", "02:30"))
	fmt.Fprintf(b, "check_read_data_subset = %q\n", envOr(getenv, "SM_BACKUP_CHECK_READ_DATA_SUBSET", "5%"))
	if v := strings.TrimSpace(getenv("SM_BACKUP_CHECK_TIME")); v != "" {
		fmt.Fprintf(b, "check_time = %q\n", v)
	}
	if v := strings.TrimSpace(getenv("SM_BACKUP_CHECK_WEEKDAY")); v != "" {
		fmt.Fprintf(b, "check_weekday = %q\n", v)
	}
}

func scheduleEnabled(getenv func(string) string) bool {
	value := strings.TrimSpace(getenv("SM_BACKUP_SCHEDULE"))
	if value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	return err != nil || enabled
}

func stageRestic(getenv func(string) string, destDir string) error {
	src := strings.TrimSpace(getenv("SM_RESTIC_SOURCE"))
	if src == "" {
		src = defaultResticInstallPath("linux")
	}
	srcData, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("bundled restic not found at %s (the agent image must ship it): %w", src, err)
	}
	dest := stagedResticPath(destDir)
	if existing, err := os.ReadFile(dest); err == nil && sha256.Sum256(existing) == sha256.Sum256(srcData) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(dest, srcData, 0o755)
}

func stagedResticPath(dir string) string {
	return filepath.Join(dir, "bin", "restic")
}

func RenderRecoveryKit(configPath string) (string, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("ServerMonitor backup recovery kit\n")
	b.WriteString("=================================\n\n")
	b.WriteString("KEEP THIS SECRET AND OFFLINE. It holds the repository password that decrypts your backups.\n")
	b.WriteString("Without it the backups are unrecoverable ciphertext, and it is never stored on the backup destination.\n\n")

	passwordFiles, reposByPassword := groupReposByFile(cfg.Repos, func(r Repo) string { return r.PasswordFile })
	if len(passwordFiles) == 0 {
		b.WriteString("No repository password file is configured for any repository.\n\n")
	}
	for _, path := range passwordFiles {
		fmt.Fprintf(&b, "Repository password for %s (contents of %s):\n\n", strings.Join(reposByPassword[path], ", "), path)
		if pw, err := os.ReadFile(path); err == nil {
			fmt.Fprintf(&b, "    %s\n\n", strings.TrimRight(string(pw), "\n"))
		} else {
			fmt.Fprintf(&b, "    <could not be read: %v>\n\n", err)
		}
	}

	b.WriteString("Repositories:\n")
	for _, repo := range cfg.Repos {
		switch {
		case repo.URL != "":
			fmt.Fprintf(&b, "  - %s -> %s\n", repo.Name, repo.URL)
		case repo.TunnelName != "":
			if repo.TunnelNode != "" {
				endpoint := "unknown"
				if cfg.Tunnel != nil {
					if node := cfg.Tunnel.Node(repo.TunnelNode); node != nil {
						endpoint = node.Endpoint
					}
				}
				fmt.Fprintf(&b, "  - %s -> tunnel:%s/%s on storage node %s (endpoint %s)\n", repo.Name, repo.TunnelNode, repo.TunnelName, repo.TunnelNode, endpoint)
			} else {
				endpoint := "unknown"
				if cfg.Tunnel != nil && cfg.Tunnel.Endpoint != "" {
					endpoint = cfg.Tunnel.Endpoint
				}
				fmt.Fprintf(&b, "  - %s -> tunnel:%s on the monitoring server (endpoint %s)\n", repo.Name, repo.TunnelName, endpoint)
			}
		}
	}

	credsFiles, reposByCreds := groupReposByFile(cfg.Repos, func(r Repo) string { return r.EnvFile })
	conventionalCreds := filepath.Join(filepath.Dir(configPath), "repo-credentials.env")
	if _, referenced := reposByCreds[conventionalCreds]; !referenced {
		if _, statErr := os.Stat(conventionalCreds); statErr == nil {
			credsFiles = append(credsFiles, conventionalCreds)
		}
	}
	for _, path := range credsFiles {
		owner := "no repository references this file"
		if names := reposByCreds[path]; len(names) > 0 {
			owner = strings.Join(names, ", ")
		}
		if creds, readErr := os.ReadFile(path); readErr == nil {
			fmt.Fprintf(&b, "\nEndpoint credentials for %s (KEEP THIS SECRET AND OFFLINE; contents of %s):\n\n    %s\n", owner, path, strings.ReplaceAll(strings.TrimRight(string(creds), "\n"), "\n", "\n    "))
		} else if !os.IsNotExist(readErr) {
			fmt.Fprintf(&b, "\nEndpoint credentials file %s could not be read: %v\n", path, readErr)
		}
	}

	b.WriteString("\nPer-repository restore instructions:\n")
	for _, repo := range cfg.Repos {
		fmt.Fprintf(&b, "\n%s:\n", repo.Name)
		if repo.URL != "" {
			fmt.Fprintf(&b, "  export RESTIC_PASSWORD='<%s above>'\n", passwordSource(repo))
			fmt.Fprintf(&b, "  restic -r %s snapshots\n", repo.URL)
			fmt.Fprintf(&b, "  restic -r %s restore latest --target /some/empty/dir\n", repo.URL)
			continue
		}
		location := "the monitoring server"
		endpoint := "unknown"
		if repo.TunnelNode != "" {
			location = "storage node " + repo.TunnelNode
			if cfg.Tunnel != nil {
				if node := cfg.Tunnel.Node(repo.TunnelNode); node != nil {
					endpoint = node.Endpoint
				}
			}
		} else if cfg.Tunnel != nil {
			endpoint = cfg.Tunnel.Endpoint
		}
		fmt.Fprintf(&b, "  1. The data physically lives on %s at endpoint %s.\n", location, endpoint)
		fmt.Fprintf(&b, "  2. The endpoint exists only inside the ServerMonitor WireGuard tunnel. From this host, or any host holding this backup.toml and tunnel.key, run:\n     sm-agent backup proxy --config %s\n     The command brings the tunnel up and prints a local RESTIC_REPOSITORY URL.\n", configPath)
		fmt.Fprintf(&b, "  3. Export RESTIC_REST_USERNAME and RESTIC_REST_PASSWORD from the endpoint credentials above, export RESTIC_PASSWORD='<%s above>', then run:\n     restic -r \"$RESTIC_REPOSITORY\" snapshots\n     restic -r \"$RESTIC_REPOSITORY\" restore latest --target /some/empty/dir\n", passwordSource(repo))
		fmt.Fprintf(&b, "  4. If the original host is gone, install sm-agent on a recovery machine, register it with the same monitoring server, recreate backup.toml with tunnel_name = %q", repo.TunnelName)
		if repo.TunnelNode != "" {
			fmt.Fprintf(&b, " and tunnel_node = %q", repo.TunnelNode)
		}
		b.WriteString(", run sm-agent backup tunnel-enroll, then run the proxy command above.\n")
	}
	return b.String(), nil
}

func groupReposByFile(repos []Repo, pick func(Repo) string) ([]string, map[string][]string) {
	order := []string{}
	byFile := map[string][]string{}
	for _, repo := range repos {
		path := strings.TrimSpace(pick(repo))
		if path == "" {
			continue
		}
		if _, seen := byFile[path]; !seen {
			order = append(order, path)
		}
		byFile[path] = append(byFile[path], repo.Name)
	}
	return order, byFile
}

func passwordSource(repo Repo) string {
	if path := strings.TrimSpace(repo.PasswordFile); path != "" {
		return "password for " + path
	}
	return "the password"
}

func isCredentialRepo(url string) bool {
	for _, scheme := range []string{"s3:", "b2:", "rest:"} {
		if strings.HasPrefix(url, scheme) {
			return true
		}
	}
	return false
}

func hasRemoteScheme(url string) bool {
	for _, scheme := range []string{"rest:", "s3:", "b2:", "gs:", "azure:", "swift:"} {
		if strings.HasPrefix(url, scheme) {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
