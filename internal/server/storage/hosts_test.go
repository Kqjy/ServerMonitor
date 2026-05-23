package storage

import (
	"crypto/sha256"
	"testing"
)

func TestConstantTimeLookup(t *testing.T) {
	hashOf := func(s string) [32]byte { return sha256.Sum256([]byte(s)) }
	a, b, c := hashOf("alpha"), hashOf("beta"), hashOf("gamma")
	m := map[[32]byte]int64{a: 1, b: 2, c: 3}

	hit := hashOf("beta")
	id, ok := constantTimeLookup(m, hit[:])
	if !ok || id != 2 {
		t.Fatalf("expected hit id=2, got id=%d ok=%v", id, ok)
	}

	miss := hashOf("delta")
	if id, ok := constantTimeLookup(m, miss[:]); ok || id != 0 {
		t.Fatalf("expected miss, got id=%d ok=%v", id, ok)
	}

	if _, ok := constantTimeLookup(map[[32]byte]int64{}, hit[:]); ok {
		t.Fatalf("expected miss on empty map")
	}

	if _, ok := constantTimeLookup(m, []byte{1, 2, 3}); ok {
		t.Fatalf("expected miss on length-mismatched candidate")
	}
}
