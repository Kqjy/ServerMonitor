package secretenvelope

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEnvelopeRoundTripAndBinding(t *testing.T) {
	const (
		token   = "agent-token-with-enough-entropy"
		purpose = ManagedBackupConfigPurpose
		secret  = "object-store-secret-that-must-not-leak"
	)
	envelope, err := Seal(token, purpose, []byte(secret))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(wire), secret) {
		t.Fatal("API envelope contains the plaintext secret")
	}
	plaintext, err := Open(token, purpose, envelope)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(plaintext) != secret {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if _, err := Open("another-agent-token", purpose, envelope); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong token error = %v", err)
	}
	if _, err := Open(token, purpose+"/other", envelope); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong purpose error = %v", err)
	}
}
