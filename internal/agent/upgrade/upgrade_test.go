package upgrade

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"servermonitor/pkg/agentsig"
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
