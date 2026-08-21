package ipban

import (
	"net/netip"
	"sync"
)

var alwaysProtected = []struct {
	prefix netip.Prefix
	reason string
}{
	{netip.MustParsePrefix("0.0.0.0/8"), "unspecified"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("255.255.255.255/32"), "broadcast"},
	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

func IsPrivate(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range privateRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func IsRoutable(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	for _, p := range alwaysProtected {
		if p.prefix.Contains(ip) {
			return false
		}
	}
	return !IsPrivate(ip)
}

type Guard struct {
	mu         sync.RWMutex
	allowlist  []netip.Prefix
	local      map[netip.Addr]struct{}
	banPrivate bool
}

func NewGuard() *Guard {
	return &Guard{local: make(map[netip.Addr]struct{})}
}

func (g *Guard) SetAllowlist(prefixes []netip.Prefix) {
	g.mu.Lock()
	g.allowlist = append([]netip.Prefix(nil), prefixes...)
	g.mu.Unlock()
}

func (g *Guard) SetLocal(addrs []netip.Addr) {
	next := make(map[netip.Addr]struct{}, len(addrs))
	for _, a := range addrs {
		next[a.Unmap()] = struct{}{}
	}
	g.mu.Lock()
	g.local = next
	g.mu.Unlock()
}

func (g *Guard) SetBanPrivate(v bool) {
	g.mu.Lock()
	g.banPrivate = v
	g.mu.Unlock()
}

func (g *Guard) Protected(ip netip.Addr) (string, bool) {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return "invalid", true
	}
	for _, p := range alwaysProtected {
		if p.prefix.Contains(ip) {
			return p.reason, true
		}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.banPrivate && IsPrivate(ip) {
		return "private", true
	}
	if _, ok := g.local[ip]; ok {
		return "local address", true
	}
	for _, p := range g.allowlist {
		if p.Contains(ip) {
			return "allowlisted", true
		}
	}
	return "", false
}

func ParseAllowlist(entries []string) ([]netip.Prefix, []string) {
	out := make([]netip.Prefix, 0, len(entries))
	var bad []string
	for _, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		bad = append(bad, e)
	}
	return out, bad
}
