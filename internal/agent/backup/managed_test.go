package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"servermonitor/pkg/wire"
)

func TestApplyManagedBackupConfigKeepsSecretsOutOfMetadataAndMerges(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "state", "backup-status.json")
	remote := wire.ManagedBackupConfig{
		Version: 42,
		Repositories: []wire.ManagedBackupRepository{{
			ID:              7,
			Name:            "host-7",
			URL:             "s3:https://example.r2.cloudflarestorage.com/backups/hosts/7/host-7",
			S3Region:        "auto",
			S3PathStyle:     true,
			AccessKeyID:     "scoped-access-key",
			SecretAccessKey: "scoped-secret-key",
			SessionToken:    "scoped-session-token",
		}},
	}
	if err := ApplyManagedBackupConfig(statusPath, remote); err != nil {
		t.Fatalf("ApplyManagedBackupConfig: %v", err)
	}
	metadata, err := os.ReadFile(ManagedBackupConfigPath(statusPath))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	for _, secret := range []string{"scoped-access-key", "scoped-secret-key", "scoped-session-token"} {
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("managed metadata contains secret %q", secret)
		}
	}

	credentialPath := filepath.Join(ManagedBackupDir(statusPath), "repository-7.env")
	credentialData, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(credentialData), "AWS_SECRET_ACCESS_KEY=scoped-secret-key") {
		t.Fatal("credential file did not contain the delivered secret")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credentialPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("credential mode = %o, want 600", got)
		}
	}

	configPath := filepath.Join(dir, "backup.toml")
	configBody := "status_path = \"" + filepath.ToSlash(statusPath) + "\"\npaths = [\"/etc\"]\nprune_mode = \"external\"\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Name != "host-7" || cfg.Repos[0].EnvFile != credentialPath {
		t.Fatalf("managed repositories were not merged: %+v", cfg.Repos)
	}
	env, err := loadRepoEnv(cfg.Repos[0])
	if err != nil {
		t.Fatalf("loadRepoEnv: %v", err)
	}
	if env["AWS_ACCESS_KEY_ID"] != "scoped-access-key" || env["AWS_SECRET_ACCESS_KEY"] != "scoped-secret-key" {
		t.Fatalf("managed credentials not loaded: %+v", env)
	}
}

func TestApplyManagedBackupConfigRemovesRevokedCredentials(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "backup-status.json")
	first := wire.ManagedBackupConfig{Repositories: []wire.ManagedBackupRepository{{
		ID: 3, Name: "repo", URL: "s3:s3.amazonaws.com/bucket/repo",
		AccessKeyID: "key", SecretAccessKey: "secret",
	}}}
	if err := ApplyManagedBackupConfig(statusPath, first); err != nil {
		t.Fatal(err)
	}
	if err := writeStatusAtomic(statusPath, StatusFile{Repos: []RepoStatus{{Name: "repo"}, {Name: "legacy-borg"}}}); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(ManagedBackupDir(statusPath), "repository-3.env")
	if err := ApplyManagedBackupConfig(statusPath, wire.ManagedBackupConfig{Version: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatalf("revoked credential still exists: %v", err)
	}
	repositories, err := loadManagedRepos(statusPath, filepath.Join(t.TempDir(), "backup.key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 0 {
		t.Fatalf("revoked repositories = %+v", repositories)
	}
	status, err := readStatusFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Repos) != 1 || status.Repos[0].Name != "legacy-borg" {
		t.Fatalf("status after managed revocation = %+v", status.Repos)
	}
}

func TestHasManagedBackupRepositoriesRequiresAnAssignment(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "backup-status.json")
	if HasManagedBackupRepositories(statusPath) {
		t.Fatal("missing managed config must not enable backup provisioning")
	}
	if err := ApplyManagedBackupConfig(statusPath, wire.ManagedBackupConfig{Version: 1}); err != nil {
		t.Fatalf("ApplyManagedBackupConfig(empty): %v", err)
	}
	if !ManagedBackupConfigExists(statusPath) {
		t.Fatal("empty managed metadata should still be persisted for revocation cleanup")
	}
	if HasManagedBackupRepositories(statusPath) {
		t.Fatal("empty managed config must not enable backup provisioning")
	}
	if err := ApplyManagedBackupConfig(statusPath, wire.ManagedBackupConfig{
		Version: 2,
		Repositories: []wire.ManagedBackupRepository{{
			ID: 9, Name: "direct", URL: "s3:https://s3.example.test/bucket/hosts/9",
			AccessKeyID: "key", SecretAccessKey: "secret",
		}},
	}); err != nil {
		t.Fatalf("ApplyManagedBackupConfig(assigned): %v", err)
	}
	if !HasManagedBackupRepositories(statusPath) {
		t.Fatal("managed assignment should enable backup provisioning")
	}
}

func TestLoadAllowsEmptyManagedAssignmentAfterRevocation(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "backup-status.json")
	if err := ApplyManagedBackupConfig(statusPath, wire.ManagedBackupConfig{Version: 3}); err != nil {
		t.Fatalf("ApplyManagedBackupConfig(empty): %v", err)
	}
	configPath := filepath.Join(dir, "backup.toml")
	configBody := "status_path = \"" + filepath.ToSlash(statusPath) + "\"\npaths = [\"/etc\"]\nprune_mode = \"external\"\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err != nil {
		t.Fatalf("Load with an empty synchronized assignment: %v", err)
	}
	if err := os.Remove(ManagedBackupConfigPath(statusPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "at least one repo") {
		t.Fatalf("Load without managed metadata error = %v, want missing repo", err)
	}
}
