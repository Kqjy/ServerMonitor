package backup

import (
	"context"
	"strings"
	"testing"
)

func TestBackupChrootsOnlyBackupWhenHostRootSet(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	if _, err := Run(context.Background(), cfg, Options{Runner: runner, HostRoot: "/host"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sawBackup := false
	for _, call := range runner.calls {
		switch resticSubcommand(call.Args) {
		case "backup":
			sawBackup = true
			if call.Chroot != "/host" {
				t.Fatalf("backup Chroot = %q, want /host", call.Chroot)
			}
			if tmp := envValue(call.Env, "TMPDIR"); tmp == "" {
				t.Fatal("backup env is missing TMPDIR redirect while chrooted")
			}
		default:
			if call.Chroot != "" {
				t.Fatalf("%s must not chroot, got %q", resticSubcommand(call.Args), call.Chroot)
			}
		}
	}
	if !sawBackup {
		t.Fatal("no backup call recorded")
	}
}

func TestBackupDoesNotChrootOnHostInstall(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	if _, err := Run(context.Background(), cfg, Options{Runner: runner}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, call := range runner.calls {
		if call.Chroot != "" {
			t.Fatalf("host install must never chroot, got %q for %s", call.Chroot, resticSubcommand(call.Args))
		}
		if tmp := envValue(call.Env, "TMPDIR"); resticSubcommand(call.Args) == "backup" && tmp != "" {
			t.Fatalf("host install backup should not override TMPDIR, got %q", tmp)
		}
	}
}

func TestResticEnvAllowlist(t *testing.T) {
	t.Setenv("SM_TOKEN", "secret-token")
	t.Setenv("SM_BACKUP_S3_SECRET_ACCESS_KEY", "s3secret")
	t.Setenv("SOME_RANDOM_SECRET", "leak-me")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")
	env := withResticEnv(Repo{URL: "rest:https://x/repo", PasswordFile: "/k"}, "/cache", "", nil)
	joined := strings.Join(env, "\n")
	for _, leaked := range []string{"SM_TOKEN", "SOME_RANDOM_SECRET", "leak-me", "secret-token", "s3secret"} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("restic env leaked %q:\n%s", leaked, joined)
		}
	}
	if !strings.Contains(joined, "HTTPS_PROXY=http://proxy.example:8080") {
		t.Fatalf("proxy var should be allowlisted for restic:\n%s", joined)
	}
	if envValue(env, "RESTIC_REPOSITORY") != "rest:https://x/repo" {
		t.Fatalf("restic repository override missing:\n%s", joined)
	}
}
