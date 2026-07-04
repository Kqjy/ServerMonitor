package config

import (
	"strings"
	"testing"
)

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
	"DATABASE_URL":        {},
	"ADMIN_TOKEN":         {},
	"TLS_CERT_FILE":       {},
	"TLS_KEY_FILE":        {},
	"TRUST_PROXY_TLS":     {},
	"INSECURE_ALLOW_HTTP": {},
	"TRUSTED_PROXIES":     {},
	"HTTP_ADDR":           {},
	"BACKUP_ACME_DOMAIN":  {},
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
