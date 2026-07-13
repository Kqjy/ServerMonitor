package storage

import (
	"net/netip"
	"testing"
)

func TestFirstFreeAddr(t *testing.T) {
	subnet := netip.MustParsePrefix("10.83.0.0/16")
	server := netip.MustParseAddr("10.83.0.1")

	got, err := firstFreeAddr(subnet, map[netip.Addr]bool{server: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParseAddr("10.83.0.2") {
		t.Fatalf("first allocation = %s, want 10.83.0.2", got)
	}

	used := map[netip.Addr]bool{
		server:                           true,
		netip.MustParseAddr("10.83.0.2"): true,
		netip.MustParseAddr("10.83.0.4"): true,
	}
	got, err = firstFreeAddr(subnet, used)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParseAddr("10.83.0.3") {
		t.Fatalf("hole reuse allocation = %s, want 10.83.0.3", got)
	}

	tiny := netip.MustParsePrefix("10.83.0.0/30")
	full := map[netip.Addr]bool{
		netip.MustParseAddr("10.83.0.1"): true,
		netip.MustParseAddr("10.83.0.2"): true,
		netip.MustParseAddr("10.83.0.3"): true,
	}
	if _, err := firstFreeAddr(tiny, full); err == nil {
		t.Fatal("expected exhaustion error")
	}
}
