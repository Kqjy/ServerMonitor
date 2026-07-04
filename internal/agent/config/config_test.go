package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func clearIdentityEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SM_SERVER_URL", "SM_TOKEN", "SM_SERVER_PUBKEY",
		"SM_INTERVAL_S", "SM_SPOOL_PATH", "SM_AUTO_UPGRADE", "SM_BACKUP_STATUS_PATH",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadFileOnly(t *testing.T) {
	clearIdentityEnv(t)
	p := writeTempConfig(t, `server_url = "https://file.example.com"
token = "filetok"
interval_s = 15
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ServerURL != "https://file.example.com" || c.Token != "filetok" {
		t.Fatalf("unexpected identity: %q %q", c.ServerURL, c.Token)
	}
	if c.IntervalS != 15 {
		t.Fatalf("interval: got %d want 15", c.IntervalS)
	}
}

func TestLoadEnvOnlyMissingFile(t *testing.T) {
	clearIdentityEnv(t)
	t.Setenv("SM_SERVER_URL", "https://env.example.com")
	t.Setenv("SM_TOKEN", "envtok")
	missing := filepath.Join(t.TempDir(), "absent.toml")
	c, err := Load(missing)
	if err != nil {
		t.Fatalf("Load env-only: %v", err)
	}
	if c.ServerURL != "https://env.example.com" || c.Token != "envtok" {
		t.Fatalf("env identity not applied: %q %q", c.ServerURL, c.Token)
	}
	if c.IntervalS != 10 {
		t.Fatalf("default interval: got %d want 10", c.IntervalS)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	clearIdentityEnv(t)
	p := writeTempConfig(t, `server_url = "https://file.example.com"
token = "filetok"
interval_s = 20
`)
	t.Setenv("SM_SERVER_URL", "https://env.example.com")
	t.Setenv("SM_TOKEN", "envtok")
	t.Setenv("SM_INTERVAL_S", "30")
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ServerURL != "https://env.example.com" || c.Token != "envtok" || c.IntervalS != 30 {
		t.Fatalf("env did not override file: %q %q %d", c.ServerURL, c.Token, c.IntervalS)
	}
}

func TestLoadMissingBoth(t *testing.T) {
	clearIdentityEnv(t)
	missing := filepath.Join(t.TempDir(), "absent.toml")
	if _, err := Load(missing); err == nil {
		t.Fatal("expected error when neither file nor env provide identity")
	}
}

func TestLoadAutoUpgradeEnv(t *testing.T) {
	clearIdentityEnv(t)
	t.Setenv("SM_SERVER_URL", "https://env.example.com")
	t.Setenv("SM_TOKEN", "envtok")

	t.Setenv("SM_AUTO_UPGRADE", "false")
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AutoUpgradeEnabled() {
		t.Fatal("SM_AUTO_UPGRADE=false should disable auto-upgrade")
	}

	t.Setenv("SM_AUTO_UPGRADE", "true")
	c, err = Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.AutoUpgradeEnabled() {
		t.Fatal("SM_AUTO_UPGRADE=true should enable auto-upgrade")
	}
}

func TestLoadInvalidIntervalEnvIgnored(t *testing.T) {
	clearIdentityEnv(t)
	t.Setenv("SM_SERVER_URL", "https://env.example.com")
	t.Setenv("SM_TOKEN", "envtok")
	t.Setenv("SM_INTERVAL_S", "not-a-number")
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.IntervalS != 10 {
		t.Fatalf("invalid SM_INTERVAL_S should keep default 10, got %d", c.IntervalS)
	}
}

func TestLoadBackupStatusPath(t *testing.T) {
	clearIdentityEnv(t)
	p := writeTempConfig(t, `server_url = "https://file.example.com"
token = "filetok"
backup_status_path = "/tmp/custom-backup-status.json"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BackupStatusPath != "/tmp/custom-backup-status.json" {
		t.Fatalf("backup status path = %q", c.BackupStatusPath)
	}

	t.Setenv("SM_BACKUP_STATUS_PATH", "/tmp/env-backup-status.json")
	c, err = Load(p)
	if err != nil {
		t.Fatalf("Load env override: %v", err)
	}
	if c.BackupStatusPath != "/tmp/env-backup-status.json" {
		t.Fatalf("env backup status path = %q", c.BackupStatusPath)
	}
}
