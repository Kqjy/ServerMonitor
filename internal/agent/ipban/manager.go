package ipban

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"runtime"
	"sync"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

const (
	fetchTimeout       = 20 * time.Second
	fetchRetryBase     = 5 * time.Second
	fetchRetryMax      = 5 * time.Minute
	maxBufferedEvents  = 2000
	maxEventsPerReport = 500
	unbanMemory        = 24 * time.Hour
	failureQueueDepth  = 1024
)

type Options struct {
	ServerURL string
	Logger    *slog.Logger
	Fetch     func(ctx context.Context) (*wire.IPBanConfig, error)
	Now       func() time.Time
}

type unbanKey struct {
	ip netip.Addr
	at time.Time
}

type Manager struct {
	opts     Options
	logger   *slog.Logger
	now      func() time.Time
	tracker  *Tracker
	guard    *Guard
	failures chan Failure
	signals  chan int64

	failuresWindow *rateWindow
	bansWindow     *rateWindow

	mu             sync.Mutex
	supported      bool
	capNetAdmin    bool
	capKnown       bool
	applied        int64
	havePolicy     bool
	policy         wire.IPBanPolicy
	lastFetchErr   string
	fetchFailures  int
	source         *sshdSource
	sourceCancel   context.CancelFunc
	sourceDone     chan struct{}
	enforcer       Enforcer
	enforceState   string
	enforceMessage string
	fleetEntries   []wire.IPBanFleetEntry
	fleetApplied   int
	events         []wire.IPBanEvent
	droppedEvents  int
	droppedFails   int
	seenUnbans     map[unbanKey]time.Time
}

func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Manager{
		opts:           opts,
		logger:         opts.Logger,
		now:            opts.Now,
		tracker:        NewTracker(Policy{}),
		guard:          NewGuard(),
		failures:       make(chan Failure, failureQueueDepth),
		signals:        make(chan int64, 1),
		failuresWindow: newRateWindow(time.Minute),
		bansWindow:     newRateWindow(time.Hour),
		supported:      runtime.GOOS == "linux",
		seenUnbans:     make(map[unbanKey]time.Time),
	}
}

func (m *Manager) Signal(version int64) {
	select {
	case m.signals <- version:
	default:
		select {
		case <-m.signals:
		default:
		}
		select {
		case m.signals <- version:
		default:
		}
	}
}

func (m *Manager) AppliedVersion() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applied
}

func (m *Manager) Run(ctx context.Context) {
	has, known := HasNetAdmin()
	m.mu.Lock()
	m.capNetAdmin, m.capKnown = has, known
	m.mu.Unlock()

	fetchTimer := time.NewTimer(0)
	defer fetchTimer.Stop()
	prune := time.NewTicker(time.Minute)
	defer prune.Stop()
	refresh := time.NewTicker(5 * time.Minute)
	defer refresh.Stop()

	for {
		select {
		case <-ctx.Done():
			m.shutdown()
			return
		case <-fetchTimer.C:
			m.fetch(ctx, fetchTimer)
		case v := <-m.signals:
			if v != m.AppliedVersion() {
				m.fetch(ctx, fetchTimer)
			}
		case f := <-m.failures:
			m.handleFailure(f)
		case <-prune.C:
			m.tracker.Prune(m.now())
			m.pruneUnbans()
			m.retryEnforcer()
		case <-refresh.C:
			m.refreshLocalAddrs(ctx)
		}
	}
}

func (m *Manager) fetch(ctx context.Context, timer *time.Timer) {
	if m.opts.Fetch == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	cfg, err := m.opts.Fetch(fctx)
	cancel()
	if err != nil {
		m.mu.Lock()
		m.fetchFailures++
		m.lastFetchErr = err.Error()
		n := m.fetchFailures
		m.mu.Unlock()
		wait := fetchRetryBase << uint(min(n-1, 6))
		if wait > fetchRetryMax {
			wait = fetchRetryMax
		}
		if n == 1 || n%10 == 0 {
			m.logger.Warn("ipban config fetch failed", "err", err, "retry_in", wait)
		}
		timer.Reset(wait)
		return
	}
	m.mu.Lock()
	m.fetchFailures = 0
	m.lastFetchErr = ""
	m.mu.Unlock()
	if cfg != nil {
		m.apply(ctx, cfg)
	}
}

func (m *Manager) apply(ctx context.Context, cfg *wire.IPBanConfig) {
	pol := cfg.Policy
	m.tracker.SetPolicy(Policy{
		MaxRetry:   pol.MaxRetry,
		FindTime:   time.Duration(pol.FindTimeS) * time.Second,
		BanTime:    time.Duration(pol.BanTimeS) * time.Second,
		BanTimeMax: time.Duration(pol.BanTimeMaxS) * time.Second,
	})
	prefixes, bad := ParseAllowlist(cfg.Allowlist)
	if len(bad) > 0 {
		m.logger.Warn("ipban allowlist entries ignored", "entries", bad)
	}
	m.guard.SetAllowlist(prefixes)
	m.guard.SetBanPrivate(pol.BanPrivate)

	m.mu.Lock()
	first := !m.havePolicy
	prevEnforce := m.havePolicy && m.policy.Enforce
	m.policy = pol
	m.havePolicy = true
	m.applied = cfg.Version
	source := m.source
	m.mu.Unlock()

	if source != nil {
		source.aggressive.Store(pol.Mode == "aggressive")
	}
	if first {
		m.refreshLocalAddrs(ctx)
	}
	if pol.Detect {
		m.startSource(ctx, pol.Mode == "aggressive")
	} else {
		m.stopSource()
	}
	if pol.Enforce {
		if m.ensureEnforcer() && !prevEnforce {
			m.syncLocalFromTracker()
		}
	} else {
		m.disableEnforcement()
	}
	m.applyUnbans(cfg.Unban)
	m.applyFleet(cfg.Fleet)
	m.logger.Info("ipban policy applied", "version", cfg.Version, "detect", pol.Detect, "enforce", pol.Enforce, "fleet", len(cfg.Fleet), "allowlist", len(prefixes))
}

func (m *Manager) startSource(ctx context.Context, aggressive bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != nil {
		return
	}
	src := newSSHDSource(m.logger, m.enqueue, m.now)
	src.aggressive.Store(aggressive)
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Run(sctx)
	}()
	m.source = src
	m.sourceCancel = cancel
	m.sourceDone = done
}

func (m *Manager) stopSource() {
	m.mu.Lock()
	cancel, done := m.sourceCancel, m.sourceDone
	m.source, m.sourceCancel, m.sourceDone = nil, nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (m *Manager) enqueue(f Failure) {
	select {
	case m.failures <- f:
	default:
		m.mu.Lock()
		m.droppedFails++
		m.mu.Unlock()
	}
}

func (m *Manager) ensureEnforcer() bool {
	m.mu.Lock()
	if m.enforcer != nil && m.enforceState == enforceOK {
		m.mu.Unlock()
		return true
	}
	if !m.supported {
		m.enforceState, m.enforceMessage = enforceUnsupported, "ip banning is only supported on Linux agents"
		m.mu.Unlock()
		return false
	}
	if m.capKnown && !m.capNetAdmin {
		m.enforceState = enforceNoPermission
		m.enforceMessage = "agent lacks CAP_NET_ADMIN, so bans cannot be written to nftables; reinstall with --enable-ipban (SM_ENABLE_IPBAN=1)"
		m.mu.Unlock()
		return false
	}
	existing := m.enforcer
	m.enforcer = nil
	m.mu.Unlock()
	if existing != nil {
		_ = existing.Close()
	}
	e, err := NewEnforcer()
	if err == nil {
		err = e.Setup()
	}
	if err != nil {
		if e != nil {
			_ = e.Close()
		}
		kind := classifyEnforceError(err)
		message := "nftables setup failed: " + err.Error()
		switch kind {
		case enforceNoPermission:
			message = "nftables refused the agent despite CAP_NET_ADMIN (AppArmor or another LSM is probably confining it): " + err.Error()
		case enforceUnsupported:
			message = "the kernel does not expose nf_tables, so bans cannot be enforced: " + err.Error()
		}
		m.mu.Lock()
		m.enforceState, m.enforceMessage = kind, message
		m.mu.Unlock()
		m.logger.Warn("ipban enforcement unavailable", "state", kind, "err", err)
		return false
	}
	local, fleet, err := e.Active()
	if err != nil {
		m.logger.Warn("ipban could not read existing nftables sets", "err", err)
	}
	now := m.now()
	for _, entry := range local {
		if entry.Expires > 0 {
			m.tracker.Import(entry.IP, now.Add(entry.Expires))
		}
	}
	m.mu.Lock()
	m.enforcer = e
	m.enforceState, m.enforceMessage = enforceOK, ""
	m.fleetApplied = len(fleet)
	m.mu.Unlock()
	m.logger.Info("ipban enforcement ready", "table", "inet "+TableName, "local", len(local), "fleet", len(fleet))
	return true
}

func (m *Manager) setEnforceError(err error) {
	kind := classifyEnforceError(err)
	m.mu.Lock()
	m.enforceState = kind
	m.enforceMessage = "nftables update failed: " + err.Error()
	m.mu.Unlock()
	m.logger.Warn("ipban enforcement error", "state", kind, "err", err)
}

func (m *Manager) currentEnforcer() Enforcer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enforceState != enforceOK {
		return nil
	}
	return m.enforcer
}

func (m *Manager) retryEnforcer() {
	m.mu.Lock()
	want := m.havePolicy && m.policy.Enforce
	broken := m.enforceState != enforceOK
	m.mu.Unlock()
	if !want || !broken {
		return
	}
	if m.ensureEnforcer() {
		m.syncLocalFromTracker()
		m.mu.Lock()
		entries := m.fleetEntries
		m.mu.Unlock()
		m.applyFleet(entries)
	}
}

func (m *Manager) syncLocalFromTracker() {
	e := m.currentEnforcer()
	if e == nil {
		return
	}
	now := m.now()
	for _, ban := range m.tracker.ActiveBans(now) {
		if err := e.AddLocal(ban.IP, ban.Until.Sub(now)); err != nil {
			m.setEnforceError(err)
			return
		}
	}
}

func (m *Manager) disableEnforcement() {
	m.mu.Lock()
	e := m.enforcer
	state := m.enforceState
	m.mu.Unlock()
	if e == nil || state != enforceOK {
		return
	}
	if err := e.FlushLocal(); err != nil {
		m.setEnforceError(err)
		return
	}
	if err := e.ReplaceFleet(nil); err != nil {
		m.setEnforceError(err)
		return
	}
	m.mu.Lock()
	m.fleetApplied = 0
	m.mu.Unlock()
}

func (m *Manager) applyUnbans(list []wire.IPBanUnban) {
	if len(list) == 0 {
		return
	}
	e := m.currentEnforcer()
	now := m.now()
	for _, u := range list {
		ip, err := netip.ParseAddr(u.IP)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		key := unbanKey{ip: ip, at: u.At.UTC()}
		m.mu.Lock()
		_, seen := m.seenUnbans[key]
		if !seen {
			m.seenUnbans[key] = now
		}
		m.mu.Unlock()
		if seen {
			continue
		}
		was := m.tracker.Unban(ip)
		if e != nil {
			if err := e.RemoveLocal(ip); err != nil {
				m.setEnforceError(err)
				e = nil
			}
		}
		if was {
			m.pushEvent(wire.IPBanEvent{Time: now, IP: ip.String(), Action: "unban", Source: "server"})
			m.logger.Info("ipban unbanned by server", "ip", ip)
		}
	}
}

func (m *Manager) pruneUnbans() {
	cutoff := m.now().Add(-unbanMemory)
	m.mu.Lock()
	for k, seen := range m.seenUnbans {
		if seen.Before(cutoff) {
			delete(m.seenUnbans, k)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) applyFleet(entries []wire.IPBanFleetEntry) {
	m.mu.Lock()
	m.fleetEntries = entries
	want := m.havePolicy && m.policy.Enforce && m.policy.ApplyFleet
	m.mu.Unlock()
	e := m.currentEnforcer()
	if e == nil {
		return
	}
	var list []FleetEntry
	if want {
		now := m.now()
		for _, entry := range entries {
			ip, err := netip.ParseAddr(entry.IP)
			if err != nil {
				continue
			}
			ip = ip.Unmap()
			if !IsRoutable(ip) {
				continue
			}
			if _, protected := m.guard.Protected(ip); protected {
				continue
			}
			timeout := entry.ExpiresAt.Sub(now)
			if timeout <= 0 {
				continue
			}
			list = append(list, FleetEntry{IP: ip, Timeout: timeout})
			if len(list) >= maxFleetEntries {
				m.logger.Warn("ipban fleet list truncated", "limit", maxFleetEntries)
				break
			}
		}
	}
	if err := e.ReplaceFleet(list); err != nil {
		m.setEnforceError(err)
		return
	}
	m.mu.Lock()
	m.fleetApplied = len(list)
	m.mu.Unlock()
}

func (m *Manager) handleFailure(f Failure) {
	now := m.now()
	m.failuresWindow.Add(now)
	if reason, protected := m.guard.Protected(f.IP); protected {
		m.logger.Debug("ipban ignoring failure from protected address", "ip", f.IP, "reason", reason)
		return
	}
	ban, ok := m.tracker.Observe(f.IP, f.User, now)
	if !ok {
		return
	}
	m.bansWindow.Add(now)
	enforced := false
	m.mu.Lock()
	enforce := m.havePolicy && m.policy.Enforce
	m.mu.Unlock()
	if enforce {
		if e := m.currentEnforcer(); e != nil {
			if len(m.tracker.ActiveBans(now)) > maxLocalEntries {
				m.logger.Warn("ipban local ban limit reached; not enforcing", "ip", ban.IP, "limit", maxLocalEntries)
			} else if err := e.AddLocal(ban.IP, ban.Until.Sub(now)); err != nil {
				m.setEnforceError(err)
			} else {
				enforced = true
			}
		}
	}
	until := ban.Until
	m.pushEvent(wire.IPBanEvent{
		Time:      now,
		IP:        ban.IP.String(),
		Action:    "ban",
		Source:    f.Source,
		Failures:  ban.Failures,
		User:      ban.User,
		ExpiresAt: &until,
		Enforced:  enforced,
		Count:     ban.Count,
	})
	m.logger.Info("ipban ban", "ip", ban.IP, "failures", ban.Failures, "user", ban.User, "until", until, "enforced", enforced, "repeat", ban.Count)
}

func (m *Manager) pushEvent(ev wire.IPBanEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) >= maxBufferedEvents {
		m.events = m.events[1:]
		m.droppedEvents++
	}
	m.events = append(m.events, ev)
}

func (m *Manager) refreshLocalAddrs(ctx context.Context) {
	var addrs []netip.Addr
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifaces {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					addrs = append(addrs, ip.Unmap())
				}
			}
		}
	}
	if u, err := url.Parse(m.opts.ServerURL); err == nil && u.Hostname() != "" {
		host := u.Hostname()
		if ip, err := netip.ParseAddr(host); err == nil {
			addrs = append(addrs, ip.Unmap())
		} else {
			lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if resolved, err := net.DefaultResolver.LookupNetIP(lctx, "ip", host); err == nil {
				for _, ip := range resolved {
					addrs = append(addrs, ip.Unmap())
				}
			}
			cancel()
		}
	}
	m.guard.SetLocal(addrs)
}

func (m *Manager) shutdown() {
	m.stopSource()
	m.mu.Lock()
	e := m.enforcer
	m.enforcer = nil
	m.mu.Unlock()
	if e != nil {
		_ = e.Close()
	}
}

func (m *Manager) Report() *wire.IPBanReport {
	now := m.now()
	active := len(m.tracker.ActiveBans(now))
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &wire.IPBanReport{
		Supported:      m.supported,
		AppliedVersion: m.applied,
		ActiveLocal:    active,
		FleetApplied:   m.fleetApplied,
	}
	switch {
	case !m.supported:
		r.Detect, r.Enforce = "unsupported", "unsupported"
		r.DetectMessage = "ip banning is only supported on Linux agents"
	case !m.havePolicy:
		r.Detect, r.Enforce = "pending", "pending"
		r.DetectMessage = "waiting for the server policy"
		if m.lastFetchErr != "" {
			r.DetectMessage = "policy fetch failing: " + m.lastFetchErr
		}
	default:
		if !m.policy.Detect {
			r.Detect = "disabled"
		} else if m.source != nil {
			st := m.source.Status()
			r.Detect = st.State
			r.DetectMessage = st.Message
			r.Sources = []wire.IPBanSourceStatus{{Name: st.Name, State: st.State, Message: st.Message}}
		} else {
			r.Detect = sourceStarting
		}
		switch {
		case !m.policy.Enforce:
			r.Enforce = "observe"
		case m.enforceState == "":
			r.Enforce = "starting"
		default:
			r.Enforce = m.enforceState
			r.EnforceMessage = m.enforceMessage
		}
	}
	n := len(m.events)
	if n > maxEventsPerReport {
		n = maxEventsPerReport
	}
	if n > 0 {
		r.Events = append([]wire.IPBanEvent(nil), m.events[:n]...)
		m.events = append([]wire.IPBanEvent(nil), m.events[n:]...)
	}
	return r
}

func (m *Manager) Status() wire.CollectorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !m.supported:
		return wire.CollectorStatus{State: "unsupported", Message: "ip banning is only supported on Linux agents"}
	case !m.havePolicy:
		msg := "waiting for the server policy"
		if m.lastFetchErr != "" {
			msg = "policy fetch failing: " + m.lastFetchErr
		}
		return wire.CollectorStatus{State: "pending", Message: msg}
	case !m.policy.Detect:
		return wire.CollectorStatus{State: "disabled"}
	}
	if m.source != nil {
		st := m.source.Status()
		switch st.State {
		case sourceNoPermission, sourceUnavailable:
			return wire.CollectorStatus{State: "no_journal", Message: st.Message}
		case sourceError:
			return wire.CollectorStatus{State: "error", Message: st.Message}
		}
	}
	if m.policy.Enforce {
		switch m.enforceState {
		case enforceNoPermission:
			return wire.CollectorStatus{State: "no_caps", Message: m.enforceMessage}
		case enforceUnsupported:
			return wire.CollectorStatus{State: "unsupported", Message: m.enforceMessage}
		case enforceError:
			return wire.CollectorStatus{State: "error", Message: m.enforceMessage}
		}
		return wire.CollectorStatus{State: "ok"}
	}
	return wire.CollectorStatus{State: "observe"}
}

func (m *Manager) Points(now time.Time) []wire.Point {
	m.mu.Lock()
	active := m.havePolicy && m.policy.Detect
	fleet := m.fleetApplied
	m.mu.Unlock()
	if !active {
		return nil
	}
	labels := map[string]string{"source": "sshd"}
	return []wire.Point{
		{Time: now, Metric: metrics.IPBanAuthFailuresPerMin, Labels: labels, Value: float64(m.failuresWindow.Count(now))},
		{Time: now, Metric: metrics.IPBanBansPerHour, Value: float64(m.bansWindow.Count(now))},
		{Time: now, Metric: metrics.IPBanActiveLocal, Value: float64(len(m.tracker.ActiveBans(now)))},
		{Time: now, Metric: metrics.IPBanFleetApplied, Value: float64(fleet)},
	}
}
