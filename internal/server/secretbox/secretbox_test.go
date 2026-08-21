package secretbox

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealOpenAndAssociatedData(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeySize)
	box, err := ParseKey(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	sealed, err := box.Seal([]byte("credential"), []byte("destination:7"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	plain, err := box.Open(sealed, []byte("destination:7"))
	if err != nil || string(plain) != "credential" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	if _, err := box.Open(sealed, []byte("destination:8")); err == nil {
		t.Fatal("ciphertext must be bound to its record")
	}
}

func TestParseKeyRejectsWrongSize(t *testing.T) {
	if _, err := ParseKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected short key to be rejected")
	}
}
