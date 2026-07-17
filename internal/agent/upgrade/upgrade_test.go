package upgrade

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"servermonitor/pkg/agentsig"
	"servermonitor/pkg/version"
)

func TestVerifyDirSafeAcceptsNormalDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := verifyDirSafe(dir); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestVerifyDirSafeRejectsWorldWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not enforced on windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	err := verifyDirSafe(dir)
	if err == nil {
		t.Fatal("expected error for world-writable dir")
	}
	if !strings.Contains(err.Error(), "world-writable") {
		t.Fatalf("expected world-writable error, got %v", err)
	}
}

func TestVerifyDirSafeRejectsMissingDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("verifyDirSafe is a no-op on windows")
	}
	err := verifyDirSafe(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestVerifyDirSafeWindowsNoop(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only check")
	}
	if err := verifyDirSafe(`Z:\does\not\exist`); err != nil {
		t.Fatalf("expected nil on windows, got %v", err)
	}
}

func TestVerifyStagedBinaryHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sm-agent.new")
	body := []byte("fake-binary-content-bytes")
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	want, _, err := agentsig.DigestReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if err := verifyStagedBinary(path, want); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestVerifyStagedBinaryDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sm-agent.new")
	original := []byte("original-binary-bytes")
	tampered := []byte("tampered-binary-bytes")
	if err := os.WriteFile(path, tampered, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	want, _, err := agentsig.DigestReader(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	err = verifyStagedBinary(path, want)
	if err == nil {
		t.Fatal("expected error when on-disk bytes differ from want")
	}
	if !strings.Contains(err.Error(), "changed between write and verify") {
		t.Fatalf("expected tamper error, got %v", err)
	}
}

func TestVerifyStagedBinaryMissingFile(t *testing.T) {
	err := verifyStagedBinary(filepath.Join(t.TempDir(), "missing"), []byte{0x00})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestWriteSignatureAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sm-agent.sig")
	if err := writeSignatureAtomic(path, "  ed25519:abc123  "); err != nil {
		t.Fatalf("writeSignatureAtomic: %v", err)
	}
	if err := writeSignatureAtomic(path, "ed25519:def456"); err != nil {
		t.Fatalf("replace signature: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read signature: %v", err)
	}
	if string(got) != "ed25519:def456\n" {
		t.Fatalf("signature contents = %q", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary signature file was left behind: %v", err)
	}
}

func TestCopyStagedBinaryRejectsSuspiciouslySmallSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "resident")
	staged := filepath.Join(dir, "privileged.new")
	if err := os.WriteFile(source, bytes.Repeat([]byte{'x'}, 100), 0o700); err != nil {
		t.Fatalf("write source: %v", err)
	}
	_, _, err := copyStagedBinary(source, staged)
	if err == nil || !strings.Contains(err.Error(), "suspiciously small") {
		t.Fatalf("copyStagedBinary error = %v", err)
	}
}

func TestManagedEnvTruthy(t *testing.T) {
	t.Setenv("SM_EXTERNALLY_MANAGED", "true")
	managed, reason := Managed()
	if !managed {
		t.Fatal("expected managed=true when SM_EXTERNALLY_MANAGED=true")
	}
	if !strings.Contains(reason, "SM_EXTERNALLY_MANAGED") {
		t.Fatalf("expected env reason, got %q", reason)
	}
}

func TestManagedEnvFalseAuthoritative(t *testing.T) {
	t.Setenv("SM_EXTERNALLY_MANAGED", "false")
	managed, reason := Managed()
	if managed {
		t.Fatalf("expected managed=false when SM_EXTERNALLY_MANAGED=false (explicit env overrides container detection), got reason %q", reason)
	}
}

func TestManagedEnvUnparseableTreatedAsManaged(t *testing.T) {
	t.Setenv("SM_EXTERNALLY_MANAGED", "yes")
	managed, reason := Managed()
	if !managed {
		t.Fatal("expected managed=true for a set but non-boolean SM_EXTERNALLY_MANAGED rather than a silent fall-through")
	}
	if !strings.Contains(reason, "yes") {
		t.Fatalf("expected reason to surface the offending value, got %q", reason)
	}
}

func writePrivilegedSyncFixture(t *testing.T, targetMode os.FileMode) (string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "sm-agent")
	source := filepath.Join(dir, "resident-agent")
	signature := filepath.Join(dir, "resident-agent.sig")
	pubkey := filepath.Join(dir, "agent-signing.pub")
	if err := os.WriteFile(target, bytes.Repeat([]byte{'t'}, 2048), targetMode); err != nil {
		t.Fatalf("write target: %v", err)
	}
	body := bytes.Repeat([]byte{'s'}, 2048)
	if err := os.WriteFile(source, body, 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	digest, _, err := agentsig.DigestReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("digest source: %v", err)
	}
	signer, err := agentsig.NewSigner()
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	if err := os.WriteFile(signature, []byte(signer.SignDigest(digest)), 0o600); err != nil {
		t.Fatalf("write signature: %v", err)
	}
	if err := os.WriteFile(pubkey, []byte(signer.PublicKeyHex()), 0o600); err != nil {
		t.Fatalf("write pubkey: %v", err)
	}
	return target, source, signature, pubkey
}

func TestSyncPrivilegedPromotionMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not enforced on windows")
	}
	target, source, signature, pubkey := writePrivilegedSyncFixture(t, 0o500)
	updated, err := syncPrivilegedTarget(context.Background(), target, source, signature, pubkey, slog.Default(), func(context.Context, string) (string, error) {
		return "999.0.0", nil
	})
	if err != nil {
		t.Fatalf("syncPrivilegedTarget: %v", err)
	}
	if !updated {
		t.Fatal("updated = false, want true")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %#o, want 0755", info.Mode().Perm())
	}
	if privilegedRunningAsRoot() && privilegedOwnerNeedsRepair(info) {
		t.Fatal("root sync did not converge target ownership")
	}
}

func TestSyncPrivilegedEqualVersionRepairsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not enforced on windows")
	}
	target, source, signature, pubkey := writePrivilegedSyncFixture(t, 0o500)
	updated, err := syncPrivilegedTarget(context.Background(), target, source, signature, pubkey, slog.Default(), func(context.Context, string) (string, error) {
		return version.Version, nil
	})
	if err != nil {
		t.Fatalf("syncPrivilegedTarget: %v", err)
	}
	if updated {
		t.Fatal("updated = true, want false")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %#o, want 0755", info.Mode().Perm())
	}
	if privilegedRunningAsRoot() && privilegedOwnerNeedsRepair(info) {
		t.Fatal("root sync did not converge target ownership")
	}
}
