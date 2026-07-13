package wgtunnel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

type Key [32]byte

func GenerateKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("generate wireguard key: %w", err)
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	return k, nil
}

func ParseKey(s string) (Key, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Key{}, fmt.Errorf("parse wireguard key: %w", err)
	}
	if len(raw) != 32 {
		return Key{}, fmt.Errorf("parse wireguard key: expected 32 bytes, got %d", len(raw))
	}
	var k Key
	copy(k[:], raw)
	return k, nil
}

func KeyFromBytes(raw []byte) (Key, error) {
	if len(raw) != 32 {
		return Key{}, fmt.Errorf("wireguard key: expected 32 bytes, got %d", len(raw))
	}
	var k Key
	copy(k[:], raw)
	return k, nil
}

func (k Key) Public() Key {
	pub, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		return Key{}
	}
	var out Key
	copy(out[:], pub)
	return out
}

func (k Key) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

func (k Key) IsZero() bool { return k == Key{} }

func (k Key) hex() string { return hex.EncodeToString(k[:]) }

func parseHexKey(s string) (Key, error) {
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return Key{}, fmt.Errorf("parse wireguard ipc key %q", s)
	}
	var k Key
	copy(k[:], raw)
	return k, nil
}
