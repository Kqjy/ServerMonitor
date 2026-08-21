package secretenvelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	Version                    = 1
	Algorithm                  = "A256GCM-SHA256"
	ManagedBackupConfigPurpose = "/api/v1/agent/backup-config"
)

var ErrInvalidEnvelope = errors.New("invalid secret envelope")

type Envelope struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func Seal(token, purpose string, plaintext []byte) (Envelope, error) {
	aead, err := newAEAD(token, purpose)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, fmt.Errorf("generate secret envelope nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte(purpose))
	return Envelope{
		Version:    Version,
		Algorithm:  Algorithm,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func Open(token, purpose string, envelope Envelope) ([]byte, error) {
	if envelope.Version != Version || envelope.Algorithm != Algorithm {
		return nil, ErrInvalidEnvelope
	}
	aead, err := newAEAD(token, purpose)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrInvalidEnvelope
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	return plaintext, nil
}

func newAEAD(token, purpose string) (cipher.AEAD, error) {
	if token == "" || purpose == "" {
		return nil, errors.New("secret envelope token and purpose are required")
	}
	key := sha256.Sum256([]byte("servermonitor:secret-envelope:v1\x00" + purpose + "\x00" + token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
