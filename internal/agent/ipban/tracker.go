package ipban

import (
	"net/netip"
	"sort"
	"sync"
	"time"
)

const escalationWindow = 7 * 24 * time.Hour

type Policy struct {
	MaxRetry   int
	FindTime   time.Duration
	BanTime    time.Duration
	BanTimeMax time.Duration
}

func (p Policy) normalized() Policy {
	if p.MaxRetry < 1 {
		p.MaxRetry = 1
	}
	if p.FindTime <= 0 {
		p.FindTime = 10 * time.Minute
	}
	if p.BanTime <= 0 {
		p.BanTime = time.Hour
	}
	if p.BanTimeMax < p.BanTime {
		p.BanTimeMax = p.BanTime
	}
	return p
}

type ipState struct {
	failures    []time.Time
	bans        int
	lastBan     time.Time
	bannedUntil time.Time
	lastUser    string
}

type Ban struct {
	IP       netip.Addr
	Until    time.Time
	Failures int
	User     string
	Count    int
}

type Tracker struct {
	mu     sync.Mutex
	policy Policy
	states map[netip.Addr]*ipState
}

func NewTracker(p Policy) *Tracker {
	return &Tracker{policy: p.normalized(), states: make(map[netip.Addr]*ipState)}
}

func (t *Tracker) SetPolicy(p Policy) {
	t.mu.Lock()
	t.policy = p.normalized()
	t.mu.Unlock()
}

func (t *Tracker) Policy() Policy {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.policy
}

func (t *Tracker) Observe(ip netip.Addr, user string, now time.Time) (Ban, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[ip]
	if st == nil {
		st = &ipState{}
		t.states[ip] = st
	}
	if user != "" {
		st.lastUser = user
	}
	if st.bannedUntil.After(now) {
		return Ban{}, false
	}
	cutoff := now.Add(-t.policy.FindTime)
	kept := st.failures[:0]
	for _, f := range st.failures {
		if f.After(cutoff) {
			kept = append(kept, f)
		}
	}
	st.failures = append(kept, now)
	if len(st.failures) < t.policy.MaxRetry {
		return Ban{}, false
	}
	if !st.lastBan.IsZero() && now.Sub(st.lastBan) > escalationWindow {
		st.bans = 0
	}
	dur := t.policy.BanTime
	for i := 0; i < st.bans && dur < t.policy.BanTimeMax; i++ {
		dur *= 2
	}
	if dur > t.policy.BanTimeMax {
		dur = t.policy.BanTimeMax
	}
	failures := len(st.failures)
	st.bans++
	st.lastBan = now
	st.bannedUntil = now.Add(dur)
	st.failures = nil
	return Ban{IP: ip, Until: st.bannedUntil, Failures: failures, User: st.lastUser, Count: st.bans}, true
}

func (t *Tracker) Import(ip netip.Addr, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[ip]
	if st == nil {
		st = &ipState{}
		t.states[ip] = st
	}
	if until.After(st.bannedUntil) {
		st.bannedUntil = until
	}
}

func (t *Tracker) Unban(ip netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[ip]
	if st == nil {
		return false
	}
	wasBanned := !st.bannedUntil.IsZero()
	delete(t.states, ip)
	return wasBanned
}

func (t *Tracker) Banned(ip netip.Addr, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[ip]
	return st != nil && st.bannedUntil.After(now)
}

func (t *Tracker) ActiveBans(now time.Time) []Ban {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Ban, 0)
	for ip, st := range t.states {
		if st.bannedUntil.After(now) {
			out = append(out, Ban{IP: ip, Until: st.bannedUntil, User: st.lastUser, Count: st.bans})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	return out
}

func (t *Tracker) Prune(now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := now.Add(-t.policy.FindTime)
	removed := 0
	for ip, st := range t.states {
		if st.bannedUntil.After(now) {
			continue
		}
		recent := false
		for _, f := range st.failures {
			if f.After(cutoff) {
				recent = true
				break
			}
		}
		if recent {
			continue
		}
		if st.bans > 0 && now.Sub(st.lastBan) <= escalationWindow {
			st.failures = nil
			continue
		}
		delete(t.states, ip)
		removed++
	}
	return removed
}

func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.states)
}

type rateWindow struct {
	mu     sync.Mutex
	window time.Duration
	stamps []time.Time
}

func newRateWindow(window time.Duration) *rateWindow {
	return &rateWindow{window: window}
}

func (r *rateWindow) Add(now time.Time) {
	r.mu.Lock()
	r.stamps = append(r.stamps, now)
	r.trimLocked(now)
	r.mu.Unlock()
}

func (r *rateWindow) Count(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trimLocked(now)
	return len(r.stamps)
}

func (r *rateWindow) trimLocked(now time.Time) {
	cutoff := now.Add(-r.window)
	i := 0
	for i < len(r.stamps) && !r.stamps[i].After(cutoff) {
		i++
	}
	if i > 0 {
		r.stamps = append(r.stamps[:0], r.stamps[i:]...)
	}
}
