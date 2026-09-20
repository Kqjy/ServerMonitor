package ipban

import (
	"net/netip"
	"testing"
)

func TestGuardProtectedRanges(t *testing.T) {
	g := NewGuard()
	cases := map[string]string{
		"127.0.0.1":       "loopback",
		"::1":             "loopback",
		"169.254.10.1":    "link-local",
		"fe80::1":         "link-local",
		"224.0.0.1":       "multicast",
		"ff02::1":         "multicast",
		"0.0.0.0":         "unspecified",
		"255.255.255.255": "broadcast",
		"10.1.2.3":        "private",
		"172.16.5.5":      "private",
		"192.168.1.1":     "private",
		"100.64.0.1":      "private",
		"fd00::1":         "private",
		"::ffff:10.0.0.1": "private",
	}
	for addr, want := range cases {
		reason, protected := g.Protected(netip.MustParseAddr(addr))
		if !protected || reason != want {
			t.Errorf("%s: got (%q, %v), want %q", addr, reason, protected, want)
		}
	}
	for _, addr := range []string{"203.0.113.5", "2001:db8::1", "8.8.8.8"} {
		if reason, protected := g.Protected(netip.MustParseAddr(addr)); protected {
			t.Errorf("%s: unexpectedly protected (%s)", addr, reason)
		}
	}
}

func TestGuardBanPrivateToggle(t *testing.T) {
	g := NewGuard()
	g.SetBanPrivate(true)
	if _, protected := g.Protected(netip.MustParseAddr("192.168.1.50")); protected {
		t.Fatal("private ranges should be bannable when enabled")
	}
	if _, protected := g.Protected(netip.MustParseAddr("127.0.0.1")); !protected {
		t.Fatal("loopback stays protected")
	}
}

func TestGuardAllowlistAndLocal(t *testing.T) {
	g := NewGuard()
	prefixes, bad := ParseAllowlist([]string{"203.0.113.0/24", "2001:db8::5", "garbage", "198.51.100.7/32"})
	if len(bad) != 1 || bad[0] != "garbage" {
		t.Fatalf("bad entries %v", bad)
	}
	if len(prefixes) != 3 {
		t.Fatalf("prefixes %v", prefixes)
	}
	g.SetAllowlist(prefixes)
	g.SetLocal([]netip.Addr{netip.MustParseAddr("198.51.100.20")})
	checks := map[string]string{
		"203.0.113.77":  "allowlisted",
		"2001:db8::5":   "allowlisted",
		"198.51.100.7":  "allowlisted",
		"198.51.100.20": "local address",
	}
	for addr, want := range checks {
		reason, protected := g.Protected(netip.MustParseAddr(addr))
		if !protected || reason != want {
			t.Errorf("%s: got (%q, %v), want %q", addr, reason, protected, want)
		}
	}
	if _, protected := g.Protected(netip.MustParseAddr("198.51.100.21")); protected {
		t.Fatal("neighbour of a local address must not be protected")
	}
}

func TestMappedIPv6AllowlistPrefixMatchesIPv4(t *testing.T) {
	prefixes, bad := ParseAllowlist([]string{"::ffff:203.0.113.0/120", "::ffff:198.51.100.1/80"})
	if len(bad) != 1 || bad[0] != "::ffff:198.51.100.1/80" {
		t.Fatalf("bad = %v", bad)
	}
	if len(prefixes) != 1 || prefixes[0].String() != "203.0.113.0/24" {
		t.Fatalf("prefixes = %v", prefixes)
	}
	g := NewGuard()
	g.SetAllowlist(prefixes)
	if reason, ok := g.Protected(netip.MustParseAddr("203.0.113.77")); !ok || reason != "allowlisted" {
		t.Fatalf("mapped prefix did not protect IPv4 address: %q %v", reason, ok)
	}
}

func TestIsRoutable(t *testing.T) {
	for _, addr := range []string{"10.0.0.1", "127.0.0.1", "fe80::1", "100.64.1.1", "::"} {
		if IsRoutable(netip.MustParseAddr(addr)) {
			t.Errorf("%s should not be routable", addr)
		}
	}
	for _, addr := range []string{"203.0.113.5", "2001:db8::9"} {
		if !IsRoutable(netip.MustParseAddr(addr)) {
			t.Errorf("%s should be routable", addr)
		}
	}
}
