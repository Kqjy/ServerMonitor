package config

import (
	"strings"
	"testing"
)

func TestValidateIntervalRejectsZeroMalformedAndOverflow(t *testing.T) {
	for _, raw := range []string{"0 days", "90d", "999999999999999999999999 years"} {
		if err := ValidateInterval(raw); err == nil {
			t.Errorf("ValidateInterval(%q) unexpectedly passed", raw)
		}
	}
	if err := ValidateInterval("90 days"); err != nil {
		t.Fatalf("valid interval rejected: %v", err)
	}
}

func TestLoadTLSGate(t *testing.T) {
	required := map[string]string{
		"DATABASE_URL": "postgres://x:y@h:5432/db",
		"ADMIN_TOKEN":  "tok",
	}

	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
		wantSec bool
	}{
		{
			name:    "plain http without flags is refused",
			env:     map[string]string{},
			wantErr: "refusing to start over plaintext HTTP",
		},
		{
			name:    "INSECURE_ALLOW_HTTP starts but is insecure browser-side",
			env:     map[string]string{"INSECURE_ALLOW_HTTP": "1"},
			wantSec: false,
		},
		{
			name:    "TRUST_PROXY_TLS starts and is secure browser-side",
			env:     map[string]string{"TRUST_PROXY_TLS": "1"},
			wantSec: true,
		},
		{
			name:    "TLS files start and are secure",
			env:     map[string]string{"TLS_CERT_FILE": "cert.pem", "TLS_KEY_FILE": "key.pem"},
			wantSec: true,
		},
		{
			name:    "BACKUP_ACME_DOMAIN alone starts and is secure",
			env:     map[string]string{"BACKUP_ACME_DOMAIN": "backup.example.com"},
			wantSec: true,
		},
		{
			name:    "TLS cert without key is rejected",
			env:     map[string]string{"TLS_CERT_FILE": "cert.pem"},
			wantErr: "TLS_CERT_FILE and TLS_KEY_FILE must be set together",
		},
		{
			name:    "TLS key without cert is rejected",
			env:     map[string]string{"TLS_KEY_FILE": "key.pem"},
			wantErr: "TLS_CERT_FILE and TLS_KEY_FILE must be set together",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k := range allEnvKeys {
				t.Setenv(k, "")
			}
			for k, v := range required {
				t.Setenv(k, v)
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want err containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got := cfg.SecureBrowserSide(); got != c.wantSec {
				t.Fatalf("SecureBrowserSide()=%v want %v", got, c.wantSec)
			}
		})
	}
}

var allEnvKeys = map[string]struct{}{
	"DATABASE_URL":                 {},
	"ADMIN_TOKEN":                  {},
	"TLS_CERT_FILE":                {},
	"TLS_KEY_FILE":                 {},
	"TRUST_PROXY_TLS":              {},
	"INSECURE_ALLOW_HTTP":          {},
	"TRUSTED_PROXIES":              {},
	"HTTP_ADDR":                    {},
	"BACKUP_ACME_DOMAIN":           {},
	"BACKUP_S3_ACCESS_KEY_ID":      {},
	"BACKUP_S3_SECRET_ACCESS_KEY":  {},
	"BACKUP_S3_SESSION_TOKEN":      {},
	"BACKUP_SECRETS_KEY":           {},
	"ARCHIVE_S3_BUCKET":            {},
	"ARCHIVE_S3_REGION":            {},
	"ARCHIVE_S3_PREFIX":            {},
	"ARCHIVE_S3_ENDPOINT":          {},
	"ARCHIVE_S3_USE_PATH_STYLE":    {},
	"ARCHIVE_S3_ACCESS_KEY_ID":     {},
	"ARCHIVE_S3_SECRET_ACCESS_KEY": {},
	"ARCHIVE_S3_SESSION_TOKEN":     {},
	"S3_BUCKET":                    {},
	"S3_REGION":                    {},
	"S3_PREFIX":                    {},
	"S3_ENDPOINT":                  {},
	"S3_USE_PATH_STYLE":            {},
}

func TestLoadArchiveS3CanonicalNamesOverrideLegacyAliases(t *testing.T) {
	for k := range allEnvKeys {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://servermonitor:real-password@db:5432/servermonitor")
	t.Setenv("ADMIN_TOKEN", "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6")
	t.Setenv("INSECURE_ALLOW_HTTP", "1")
	t.Setenv("S3_BUCKET", "legacy-bucket")
	t.Setenv("S3_REGION", "legacy-region")
	t.Setenv("ARCHIVE_S3_BUCKET", "archive-bucket")
	t.Setenv("ARCHIVE_S3_REGION", "archive-region")
	t.Setenv("ARCHIVE_S3_ACCESS_KEY_ID", "archive-key")
	t.Setenv("ARCHIVE_S3_SECRET_ACCESS_KEY", "archive-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArchiveS3Bucket != "archive-bucket" || cfg.ArchiveS3Region != "archive-region" {
		t.Fatalf("canonical archive settings did not win: bucket=%q region=%q", cfg.ArchiveS3Bucket, cfg.ArchiveS3Region)
	}
	if cfg.ArchiveS3AccessKeyID != "archive-key" || cfg.ArchiveS3SecretKey != "archive-secret" {
		t.Fatal("archive-specific credentials were not loaded")
	}

	t.Setenv("ARCHIVE_S3_BUCKET", "")
	t.Setenv("ARCHIVE_S3_REGION", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load legacy aliases: %v", err)
	}
	if cfg.ArchiveS3Bucket != "legacy-bucket" || cfg.ArchiveS3Region != "legacy-region" {
		t.Fatalf("legacy aliases were not preserved: bucket=%q region=%q", cfg.ArchiveS3Bucket, cfg.ArchiveS3Region)
	}
}

func TestLoadBackupS3Credentials(t *testing.T) {
	for k := range allEnvKeys {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://servermonitor:real-password@db:5432/servermonitor")
	t.Setenv("ADMIN_TOKEN", "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6")
	t.Setenv("INSECURE_ALLOW_HTTP", "1")
	t.Setenv("BACKUP_S3_ACCESS_KEY_ID", "backup-key")
	t.Setenv("BACKUP_S3_SECRET_ACCESS_KEY", "backup-secret")
	t.Setenv("BACKUP_S3_SESSION_TOKEN", "backup-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BackupS3AccessKeyID != "backup-key" || cfg.BackupS3SecretKey != "backup-secret" || cfg.BackupS3SessionToken != "backup-token" {
		t.Fatalf("backup S3 credentials were not loaded")
	}

	t.Setenv("BACKUP_S3_SECRET_ACCESS_KEY", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("expected incomplete backup S3 credentials to be rejected, got %v", err)
	}
}

func TestLoadRejectsPlaceholderSecrets(t *testing.T) {
	cases := []struct {
		name        string
		dbURL       string
		adminToken  string
		wantErrFrag string
	}{
		{
			name:        "ADMIN_TOKEN is the .env.example placeholder",
			dbURL:       "postgres://servermonitor:hunter2@db:5432/servermonitor",
			adminToken:  "change-me-to-a-long-random-string",
			wantErrFrag: "ADMIN_TOKEN looks like the .env.example placeholder",
		},
		{
			name:        "DATABASE_URL password is the placeholder",
			dbURL:       "postgres://servermonitor:change-me-to-a-long-random-string@db:5432/servermonitor",
			adminToken:  "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
			wantErrFrag: "DATABASE_URL password looks like the .env.example placeholder",
		},
		{
			name:       "real-looking values pass the placeholder check",
			dbURL:      "postgres://servermonitor:s0me-r3al-passw0rd@db:5432/servermonitor",
			adminToken: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
		},
		{
			name:       "marker substring in hostname is not flagged",
			dbURL:      "postgres://servermonitor:s0me-r3al-passw0rd@changeme.internal:5432/servermonitor",
			adminToken: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
		},
		{
			name:       "marker substring in database name is not flagged",
			dbURL:      "postgres://servermonitor:s0me-r3al-passw0rd@db:5432/change-me-db",
			adminToken: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k := range allEnvKeys {
				t.Setenv(k, "")
			}
			t.Setenv("DATABASE_URL", c.dbURL)
			t.Setenv("ADMIN_TOKEN", c.adminToken)
			t.Setenv("INSECURE_ALLOW_HTTP", "1")
			_, err := Load()
			if c.wantErrFrag == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErrFrag) {
				t.Fatalf("want err containing %q, got %v", c.wantErrFrag, err)
			}
		})
	}
}
