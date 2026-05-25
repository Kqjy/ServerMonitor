package agentsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	SignatureHeader = "X-Agent-Signature"
	algorithm       = "ed25519"
)

var (
	ErrNoSignature   = errors.New("missing or empty signature header")
	ErrBadAlgorithm  = errors.New("unsupported signature algorithm")
	ErrBadEncoding   = errors.New("signature is not valid hex")
	ErrBadSignature  = errors.New("signature did not match the binary")
	ErrUnknownPubkey = errors.New("server_pubkey not configured in agent.toml; re-register the agent")
)

type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func NewSigner() (*Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Signer{priv: priv, pub: pub}, nil
}

func LoadOrCreateSigner(path string) (*Signer, bool, error) {
	if path == "" {
		return nil, false, errors.New("signing key path required")
	}
	data, err := os.ReadFile(path)
	if err == nil {
		s, err := SignerFromHex(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, false, fmt.Errorf("parse %s: %w", path, err)
		}
		return s, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	s, err := NewSigner()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(path, []byte(s.PrivateKeyHex()+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return s, true, nil
}

func SignerFromHex(h string) (*Signer, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		priv := ed25519.NewKeyFromSeed(raw)
		return &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
	case ed25519.PrivateKeySize:
		priv := ed25519.PrivateKey(raw)
		return &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
	default:
		return nil, fmt.Errorf("expected %d or %d hex bytes, got %d", ed25519.SeedSize, ed25519.PrivateKeySize, len(raw))
	}
}

func (s *Signer) PublicKeyHex() string {
	return hex.EncodeToString(s.pub)
}

func (s *Signer) PrivateKeyHex() string {
	seed := s.priv.Seed()
	return hex.EncodeToString(seed)
}

func (s *Signer) SignDigest(digest []byte) string {
	sig := ed25519.Sign(s.priv, digest)
	return algorithm + ":" + hex.EncodeToString(sig)
}

func NewDigest() hash.Hash {
	return sha256.New()
}

func DigestReader(r io.Reader) ([]byte, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return nil, n, err
	}
	return h.Sum(nil), n, nil
}

func Verify(pubkeyHex, header string, digest []byte) error {
	if header == "" {
		return ErrNoSignature
	}
	algo, payload, ok := strings.Cut(header, ":")
	if !ok || algo != algorithm {
		return fmt.Errorf("%w: %q", ErrBadAlgorithm, algo)
	}
	sig, err := hex.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadEncoding, err)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(pubkeyHex))
	if err != nil {
		return fmt.Errorf("decode pubkey: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("decode pubkey: expected %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), digest, sig) {
		return ErrBadSignature
	}
	return nil
}
