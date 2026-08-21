package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const KeySize = 32

var ErrNotConfigured = errors.New("backup secret encryption is not configured")

type Box struct {
	aead cipher.AEAD
}

func ParseKey(value string) (*Box, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	key, err := decodeKey(value)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func decodeKey(value string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		key, err := decode(value)
		if err == nil && len(key) == KeySize {
			return key, nil
		}
	}
	return nil, fmt.Errorf("BACKUP_SECRETS_KEY must encode exactly %d bytes (base64 or hex)", KeySize)
}

func (b *Box) Seal(plaintext, associatedData []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrNotConfigured
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, b.aead.Seal(nil, nonce, plaintext, associatedData)...), nil
}

func (b *Box) Open(ciphertext, associatedData []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrNotConfigured
	}
	if len(ciphertext) < b.aead.NonceSize() {
		return nil, errors.New("encrypted backup credential is truncated")
	}
	nonce := ciphertext[:b.aead.NonceSize()]
	return b.aead.Open(nil, nonce, ciphertext[b.aead.NonceSize():], associatedData)
}
