package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		password string
		wantErr  string
	}{
		{"short", "at least 12"},
		{"correcthorse1!", ""},
		{"password12345", "weak"},
		{"PASSWORD12345", "weak"},
		{"administrator99", "weak"},
		{"changeme-please", "weak"},
		{"a-strong-passphrase-2026", ""},
	}
	for _, c := range cases {
		t.Run(c.password, func(t *testing.T) {
			err := validatePassword(c.password)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("expected accept, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if c.wantErr == "weak" {
				if !errors.Is(err, ErrWeakPassword) {
					t.Fatalf("expected ErrWeakPassword, got %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("expected err containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
