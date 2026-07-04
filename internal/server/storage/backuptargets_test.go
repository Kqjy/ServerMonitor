package storage

import "testing"

func TestMatchTargetSecretAndRevocation(t *testing.T) {
	secret := "correct-horse-battery-staple"
	var key [32]byte
	copy(key[:], tokenHash(secret))
	active := backupTargetAuth{id: 7, hash: key, quota: 1000, used: 250, revoked: false}

	id, quota, used, ok, err := matchTarget(active, tokenHash(secret))
	if err != nil {
		t.Fatalf("matchTarget: %v", err)
	}
	if !ok || id != 7 || quota != 1000 || used != 250 {
		t.Fatalf("correct secret should match: ok=%v id=%d quota=%d used=%d", ok, id, quota, used)
	}

	if _, _, _, ok, _ := matchTarget(active, tokenHash("wrong-secret")); ok {
		t.Fatal("wrong secret must not match")
	}

	revoked := active
	revoked.revoked = true
	if _, _, _, ok, _ := matchTarget(revoked, tokenHash(secret)); ok {
		t.Fatal("revoked target must not match even with the correct secret")
	}
}
