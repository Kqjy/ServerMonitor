package api

import "testing"

func TestNormalizeQuota(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	positive := int64(1024)

	if got, err := normalizeQuota(nil); err != nil || got != nil {
		t.Fatalf("normalizeQuota(nil) = %v, %v; want nil, nil", got, err)
	}
	if got, err := normalizeQuota(&zero); err != nil || got != nil {
		t.Fatalf("normalizeQuota(0) = %v, %v; want nil, nil", got, err)
	}
	if got, err := normalizeQuota(&negative); err == nil || got != nil {
		t.Fatalf("normalizeQuota(-1) = %v, %v; want nil, error", got, err)
	}
	if got, err := normalizeQuota(&positive); err != nil || got != &positive || *got != positive {
		t.Fatalf("normalizeQuota(1024) = %v, %v; want same pointer, nil", got, err)
	}
}
