package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRenderBackupTOMLIsValid(t *testing.T) {
	getenv := envFrom(map[string]string{
		"SM_BACKUP_REPOS":                "rest:https://sv/backup/web01,s3:https://s3/bucket",
		"SM_BACKUP_REPO_NAMES":           "web01",
		"SM_BACKUP_PATHS":                "/etc,/home",
		"SM_BACKUP_S3_REGION":            "us-east-1",
		"SM_BACKUP_S3_PATH_STYLE":        "1",
		"SM_BACKUP_TIME":                 "03:15",
		"SM_BACKUP_S3_ACCESS_KEY_ID":     "AKIA",
		"SM_BACKUP_S3_SECRET_ACCESS_KEY": "secret",
	})
	toml, err := renderBackupTOML(getenv, "/state/backup/backup.key", "/state/backup/repo-credentials.env", true)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`prune_mode = "external"`,
		"[[repo]]\nname = \"web01\"\nurl = \"rest:https://sv/backup/web01\"",
		"name = \"repo2\"\nurl = \"s3:https://s3/bucket\"",
		`env_file = "/state/backup/repo-credentials.env"`,
		`s3_region = "us-east-1"`,
		`s3_path_style = true`,
		"[schedule]\nenabled = true\nbackup_time = \"03:15\"",
	} {
		if !strings.Contains(toml, want) {
			t.Fatalf("rendered toml missing %q\n%s", want, toml)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(t.TempDir(), "backup.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("generated backup.toml does not parse/validate: %v\n%s", err, toml)
	}
	if cfg.PruneMode != "external" {
		t.Fatalf("container prune_mode should default to external, got %q", cfg.PruneMode)
	}
	if cfg.Schedule == nil || !cfg.Schedule.Enabled || cfg.Schedule.BackupTime != "03:15" {
		t.Fatalf("schedule not rendered: %#v", cfg.Schedule)
	}
}

func TestRenderBackupTOMLRejectsTunnel(t *testing.T) {
	getenv := envFrom(map[string]string{"SM_BACKUP_REPOS": "tunnel:web01"})
	if _, err := renderBackupTOML(getenv, "/k", "/c", false); err == nil {
		t.Fatal("tunnel repos should be rejected in the container Phase 1 provisioner")
	}
}

func TestEnsureBackupKeyNeverRegenerates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.key")
	gen, err := ensureBackupKey(path)
	if err != nil || !gen {
		t.Fatalf("first ensure should generate: gen=%v err=%v", gen, err)
	}
	first, _ := os.ReadFile(path)
	if len(strings.TrimSpace(string(first))) == 0 {
		t.Fatal("key file is empty")
	}
	gen, err = ensureBackupKey(path)
	if err != nil || gen {
		t.Fatalf("second ensure must NOT regenerate: gen=%v err=%v", gen, err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("existing backup key must never be overwritten")
	}
}

func TestWriteCredsFileOmittedWhenNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo-credentials.env")
	have, err := writeCredsFile(envFrom(nil), path)
	if err != nil || have {
		t.Fatalf("no creds should yield have=false: have=%v err=%v", have, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("no creds file should be written when none provided")
	}
	have, err = writeCredsFile(envFrom(map[string]string{"SM_BACKUP_REST_PASSWORD": "pw"}), path)
	if err != nil || !have {
		t.Fatalf("creds present should yield have=true: have=%v err=%v", have, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "RESTIC_REST_PASSWORD=pw") {
		t.Fatalf("creds file missing value: %s", data)
	}
}
