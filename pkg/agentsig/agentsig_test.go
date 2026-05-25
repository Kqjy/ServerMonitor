package agentsig

import (
	"bytes"
	"errors"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	s, err := NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := DigestReader(bytes.NewReader([]byte("hello world")))
	if err != nil {
		t.Fatal(err)
	}
	header := s.SignDigest(digest)
	if err := Verify(s.PublicKeyHex(), header, digest); err != nil {
		t.Fatalf("verify roundtrip: %v", err)
	}
}

func TestVerifyRejectsWrongDigest(t *testing.T) {
	s, _ := NewSigner()
	a, _, _ := DigestReader(bytes.NewReader([]byte("file-a")))
	b, _, _ := DigestReader(bytes.NewReader([]byte("file-b")))
	header := s.SignDigest(a)
	if err := Verify(s.PublicKeyHex(), header, b); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsWrongPubkey(t *testing.T) {
	a, _ := NewSigner()
	b, _ := NewSigner()
	digest, _, _ := DigestReader(bytes.NewReader([]byte("payload")))
	header := a.SignDigest(digest)
	if err := Verify(b.PublicKeyHex(), header, digest); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsBadAlgorithm(t *testing.T) {
	s, _ := NewSigner()
	digest, _, _ := DigestReader(bytes.NewReader([]byte("payload")))
	header := s.SignDigest(digest)
	bad := "rsa:" + header[len("ed25519:"):]
	if err := Verify(s.PublicKeyHex(), bad, digest); !errors.Is(err, ErrBadAlgorithm) {
		t.Fatalf("expected ErrBadAlgorithm, got %v", err)
	}
}

func TestVerifyRejectsEmptyHeader(t *testing.T) {
	s, _ := NewSigner()
	digest, _, _ := DigestReader(bytes.NewReader([]byte("payload")))
	if err := Verify(s.PublicKeyHex(), "", digest); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("expected ErrNoSignature, got %v", err)
	}
}

func TestSignerFromHexSeed(t *testing.T) {
	a, _ := NewSigner()
	b, err := SignerFromHex(a.PrivateKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicKeyHex() != b.PublicKeyHex() {
		t.Fatalf("expected same pubkey after reload: %s vs %s", a.PublicKeyHex(), b.PublicKeyHex())
	}
}
