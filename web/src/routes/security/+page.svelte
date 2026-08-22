<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import {
    api,
    type IPBanSettings,
    type IPBanHost,
    type IPBanHostPolicy,
    type IPBanFleet,
    type IPBanEvent,
    type IPBanSummary,
    type IPBanStats
  } from '$lib/api';
  import { timeAgo, timeUntil, statusFor } from '$lib/format';
  import { announcer } from '$lib/announce.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import MultiChart from '$lib/components/MultiChart.svelte';

  type Tone = 'good' | 'warn' | 'bad' | 'info' | 'none';
  const toneText: Record<Tone, string> = {
    good: 'text-emerald-300',
    warn: 'text-amber-300',
    bad: 'text-rose-300',
    info: 'text-sky-300',
    none: 'text-zinc-500'
  };
  const toneDot: Record<Tone, string> = {
    good: 'bg-emerald-400',
    warn: 'bg-amber-400',
    bad: 'bg-rose-400',
    info: 'bg-sky-400',
    none: 'bg-zinc-600'
  };
  const tonePill: Record<Tone, string> = {
    good: 'border-emerald-900/60 bg-emerald-950/40 text-emerald-300',
    warn: 'border-amber-900/60 bg-amber-950/40 text-amber-300',
    bad: 'border-rose-900/60 bg-rose-950/40 text-rose-300',
    info: 'border-sky-900/60 bg-sky-950/40 text-sky-300',
    none: 'border-zinc-800 bg-zinc-950 text-zinc-500'
  };

  let summary = $state<IPBanSummary | null>(null);
  let hosts = $state<IPBanHost[]>([]);
  let fleet = $state<IPBanFleet[]>([]);
  let events = $state<IPBanEvent[]>([]);
  const evPageSizes = [8, 16, 50];
  let evPage = $state(0);
  let evPageSize = $state(evPageSizes[0]);
  const evPageCount = $derived(Math.max(1, Math.ceil(events.length / evPageSize)));
  const evPageIndex = $derived(Math.min(Math.max(evPage, 0), evPageCount - 1));
  const evFrom = $derived(evPageIndex * evPageSize);
  const evTo = $derived(Math.min(evFrom + evPageSize, events.length));
  const pagedEvents = $derived(events.slice(evFrom, evTo));
  let stats = $state<IPBanStats | null>(null);
  let statsWindow = $state('168h');
  const statsWindows = [
    { label: '24 h', value: '24h' },
    { label: '7 d', value: '168h' },
    { label: '30 d', value: '720h' },
    { label: '90 d', value: '2160h' }
  ];
  function gotoEvPage(page: number) {
    evPage = Math.min(Math.max(page, 0), evPageCount - 1);
  }

  function setEvPageSize(size: number) {
    evPageSize = size;
    evPage = 0;
  }

  const banSeries = $derived(
    stats
      ? [
          { label: 'bans', points: stats.buckets.map((b) => ({ ts: b.time, v: b.bans })) },
          { label: 'distinct addresses', points: stats.buckets.map((b) => ({ ts: b.time, v: b.distinct_ips })) }
        ]
      : []
  );
  const statsFromMs = $derived(stats ? new Date(stats.from).getTime() : 0);
  const statsToMs = $derived(stats ? new Date(stats.to).getTime() : 0);
  const onceOnlyIPs = $derived(stats ? Math.max(0, stats.distinct_ips - stats.repeat_ips) : 0);
  let settings = $state<IPBanSettings | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;
  let refreshGen = 0;
  let inflight: AbortController | null = null;

  let form = $state({
    enabled: true,
    enforce: false,
    contribute: true,
    apply_fleet: true,
    mode: 'normal',
    max_retry: 5,
    find_time_min: 10,
    ban_time_min: 60,
    ban_time_max_h: 168,
    ban_private: false,
    fleet_min_hosts: 2,
    fleet_min_bans: 3,
    fleet_ttl_h: 24,
    allowlist: ''
  });
  let formDirty = $state(false);
  let saving = $state(false);
  let saveError = $state<string | null>(null);
  let saveInfo = $state<string | null>(null);

  let banIP = $state('');
  let banTTL = $state('86400');
  let banNote = $state('');
  let banBusy = $state(false);
  let banError = $state<string | null>(null);
  let confirmManualBan = $state(false);
  let toFleetUnban = $state<IPBanFleet | null>(null);
  let policyError = $state<string | null>(null);
  let policyBusy = $state<number | null>(null);

  const allowlistLines = $derived(
    form.allowlist
      .split(/\r?\n|,/)
      .map((s) => s.trim())
      .filter((s) => s.length > 0)
  );
  const enforceBlocked = $derived(allowlistLines.length === 0);
  const clientIP = $derived(settings?.client_ip ?? '');
  const clientIPListed = $derived(clientIP !== '' && allowlistLines.some((l) => l === clientIP || l === `${clientIP}/32` || l === `${clientIP}/128`));

  function hydrate(s: IPBanSettings) {
    form = {
      enabled: s.enabled,
      enforce: s.enforce,
      contribute: s.contribute,
      apply_fleet: s.apply_fleet,
      mode: s.mode,
      max_retry: s.max_retry,
      find_time_min: Math.round(s.find_time_s / 60),
      ban_time_min: Math.round(s.ban_time_s / 60),
      ban_time_max_h: Math.round(s.ban_time_max_s / 3600),
      ban_private: s.ban_private,
      fleet_min_hosts: s.fleet_min_hosts,
      fleet_min_bans: s.fleet_min_bans,
      fleet_ttl_h: Math.round(s.fleet_ttl_s / 3600),
      allowlist: (s.allowlist ?? []).join('\n')
    };
    formDirty = false;
  }

  async function refresh(includeSettings = false) {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    try {
      const [sum, hs, fl, ev, stt, st] = await Promise.all([
        api.ipbanSummary({ signal: ac.signal }),
        api.ipbanHosts({ signal: ac.signal }),
        api.ipbanFleet({ signal: ac.signal }),
        api.ipbanEvents({ limit: 100, signal: ac.signal }),
        api.ipbanStats({ window: statsWindow, top: 12, signal: ac.signal }),
        includeSettings || !settings ? api.ipbanSettings({ signal: ac.signal }) : Promise.resolve(null)
      ]);
      if (gen !== refreshGen) return;
      summary = sum;
      hosts = hs;
      fleet = fl;
      events = ev;
      stats = stt;
      if (st) {
        settings = st;
        if (!formDirty) hydrate(st);
      }
      error = null;
    } catch (e) {
      if (gen !== refreshGen || (e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      if (gen === refreshGen) loading = false;
    }
  }

  onMount(() => {
    void refresh(true);
    timer = setInterval(() => void refresh(), 20_000);
    const sp = $page.url.searchParams;
    if (sp.get('ban') === '1') {
      banIP = sp.get('ip') ?? '';
    }
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    inflight?.abort();
  });

  function setStatsWindow(value: string) {
    if (statsWindow === value) return;
    statsWindow = value;
    stats = null;
    void refresh();
  }

  function banOffender(ip: string) {
    banIP = ip;
    banError = null;
    confirmManualBan = true;
  }

  function windowLabel(value: string): string {
    return statsWindows.find((w) => w.value === value)?.label ?? value;
  }

  function markDirty() {
    formDirty = true;
    saveInfo = null;
  }

  function addMyIP() {
    if (!clientIP || clientIPListed) return;
    form.allowlist = (form.allowlist.trim() ? form.allowlist.trimEnd() + '\n' : '') + clientIP;
    markDirty();
  }

  async function saveSettings(e: Event) {
    e.preventDefault();
    saveError = null;
    saveInfo = null;
    if (form.enforce && enforceBlocked) {
      saveError = 'Add at least one allowlisted address or network before turning enforcement on.';
      announcer.say(saveError);
      return;
    }
    saving = true;
    try {
      const saved = await api.ipbanUpdateSettings({
        enabled: form.enabled,
        enforce: form.enforce,
        contribute: form.contribute,
        apply_fleet: form.apply_fleet,
        mode: form.mode,
        max_retry: Number(form.max_retry),
        find_time_s: Math.round(Number(form.find_time_min) * 60),
        ban_time_s: Math.round(Number(form.ban_time_min) * 60),
        ban_time_max_s: Math.round(Number(form.ban_time_max_h) * 3600),
        ban_private: form.ban_private,
        fleet_min_hosts: Number(form.fleet_min_hosts),
        fleet_min_bans: Number(form.fleet_min_bans),
        fleet_ttl_s: Math.round(Number(form.fleet_ttl_h) * 3600),
        allowlist: allowlistLines
      });
      settings = saved;
      hydrate(saved);
      saveInfo = 'Saved. Agents pick the new policy up on their next check-in, within about a minute.';
      announcer.say('Security settings saved');
      void refresh();
    } catch (err) {
      saveError = (err as Error).message;
      announcer.say(`Saving failed: ${saveError}`);
    } finally {
      saving = false;
    }
  }

  function ttlLabel(s: string): string {
    switch (s) {
      case '3600':
        return '1 hour';
      case '86400':
        return '24 hours';
      case '604800':
        return '7 days';
      case '2592000':
        return '30 days';
      default:
        return `${s}s`;
    }
  }

  async function doManualBan() {
    banError = null;
    const ban = await api.ipbanManualBan({ ip: banIP.trim(), ttl_s: Number(banTTL), note: banNote.trim() });
    announcer.say(`${ban.ip} added to the fleet blocklist`);
    banIP = '';
    banNote = '';
    confirmManualBan = false;
    void refresh();
  }

  async function doFleetUnban() {
    if (!toFleetUnban) return;
    const ip = toFleetUnban.ip;
    await api.ipbanFleetUnban(ip);
    announcer.say(`${ip} removed from the fleet blocklist`);
    toFleetUnban = null;
    void refresh();
  }

  type PolicyKey = keyof IPBanHostPolicy;

  function selValue(v: boolean | null): 'inherit' | 'on' | 'off' {
    return v === null ? 'inherit' : v ? 'on' : 'off';
  }

  async function setPolicy(h: IPBanHost, key: PolicyKey, e: Event) {
    const raw = (e.currentTarget as HTMLSelectElement).value;
    const next: IPBanHostPolicy = { ...h.override, [key]: raw === 'inherit' ? null : raw === 'on' };
    policyError = null;
    policyBusy = h.host_id;
    try {
      await api.ipbanUpdateHost(h.host_id, next);
      announcer.say(`Policy updated for ${h.hostname}`);
      await refresh();
    } catch (err) {
      policyError = `${h.hostname}: ${(err as Error).message}`;
      announcer.say(policyError);
      await refresh();
    } finally {
      policyBusy = null;
    }
  }

  function detectInfo(h: IPBanHost): { label: string; tone: Tone } {
    if (!h.os.toLowerCase().includes('linux')) return { label: 'Linux only', tone: 'none' };
    switch (h.detect_state) {
      case 'ok':
        return { label: 'watching sshd', tone: 'good' };
      case 'no_permission':
        return { label: 'no log access', tone: 'warn' };
      case 'unavailable':
        return { label: 'no auth log', tone: 'warn' };
      case 'error':
        return { label: 'error', tone: 'bad' };
      case 'disabled':
        return { label: 'off', tone: 'none' };
      case 'pending':
        return { label: 'waiting for policy', tone: 'none' };
      case 'starting':
        return { label: 'starting', tone: 'none' };
      case '':
        return { label: h.supported ? 'no report' : 'agent too old', tone: 'none' };
      default:
        return { label: h.detect_state, tone: 'none' };
    }
  }

  function enforceInfo(h: IPBanHost): { label: string; tone: Tone } {
    if (!h.os.toLowerCase().includes('linux')) return { label: '—', tone: 'none' };
    if (h.enforce_state === '') return { label: '—', tone: 'none' };
    if (!h.effective.enforce) return { label: 'observing', tone: 'info' };
    switch (h.enforce_state) {
      case 'ok':
        return { label: 'enforcing', tone: 'good' };
      case 'no_permission':
        return { label: 'needs CAP_NET_ADMIN', tone: 'warn' };
      case 'unsupported':
        return { label: 'no nftables', tone: 'warn' };
      case 'error':
        return { label: 'error', tone: 'bad' };
      default:
        return { label: h.enforce_state, tone: 'none' };
    }
  }

  const liveHosts = $derived(hosts.filter((h) => !h.archived));
  const attention = $derived(
    liveHosts.filter((h) => {
      if (!h.os.toLowerCase().includes('linux')) return false;
      if (h.detect_state === 'no_permission' || h.detect_state === 'unavailable' || h.detect_state === 'error') return true;
      if (h.effective.enforce && (h.enforce_state === 'no_permission' || h.enforce_state === 'unsupported' || h.enforce_state === 'error')) return true;
      return h.config_stale;
    })
  );

  function attentionText(h: IPBanHost): string {
    if (h.detect_state === 'no_permission') return 'cannot read the journal or auth log — reinstall with SM_ENABLE_IPBAN=1';
    if (h.detect_state === 'unavailable') return 'no sshd log source found — ' + (h.detect_message ?? '');
    if (h.detect_state === 'error') return 'detection error — ' + (h.detect_message ?? '');
    if (h.enforce_state === 'no_permission') return 'bans cannot be written to nftables — reinstall with SM_ENABLE_IPBAN=1';
    if (h.enforce_state === 'unsupported') return 'kernel has no nf_tables — ' + (h.enforce_message ?? '');
    if (h.enforce_state === 'error') return 'nftables error — ' + (h.enforce_message ?? '');
    if (h.config_stale) return `still on policy version ${h.applied_version}, current is ${settings?.version ?? '?'}`;
    return '';
  }

  function actionLabel(action: string, enforced: boolean): { text: string; tone: Tone } {
    switch (action) {
      case 'ban':
        return enforced ? { text: 'banned', tone: 'bad' } : { text: 'would ban', tone: 'info' };
      case 'unban':
        return { text: 'unbanned', tone: 'good' };
      case 'fleet_ban':
        return { text: 'fleet ban', tone: 'warn' };
      case 'fleet_unban':
        return { text: 'fleet unban', tone: 'good' };
      case 'manual_ban':
        return { text: 'manual ban', tone: 'warn' };
      case 'manual_unban':
        return { text: 'unban requested', tone: 'info' };
      default:
        return { text: action, tone: 'none' };
    }
  }

  function absTime(iso: string): string {
    return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  const inputClass = 'w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric';
  const selectClass = 'rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-2 py-1 text-xs';
</script>

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex items-start justify-between gap-3 flex-wrap">
    <div>
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Security</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1">
        Reactive IP banning across the fleet: agents watch sshd login failures, block repeat offenders with nftables, and share confirmed attackers with every other host.
      </p>
    </div>
  </div>

  {#if error}
    <div class="mt-4 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
  {/if}

  {#if loading && !summary}
    <div class="mt-6 h-40 rounded-xl shimmer"></div>
  {:else if summary}
    <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <div class="grid grid-cols-2 lg:grid-cols-4 divide-y lg:divide-y-0 divide-zinc-800 [&>div:nth-child(odd)]:border-r [&>div]:border-zinc-800 lg:[&>div:not(:last-child)]:border-r">
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Watching sshd</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{summary.hosts_detecting}<span class="text-sm text-zinc-500 font-normal"> / {summary.hosts_total}</span></div>
          <div class="text-[11px] text-zinc-500 mt-0.5">
            {#if summary.hosts_not_capable > 0}<span class="text-amber-300">{summary.hosts_not_capable} not capable</span>{:else}hosts reporting detection{/if}
          </div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Enforcing</div>
          <div class="text-2xl font-semibold numeric mt-1 {summary.hosts_enforcing > 0 ? 'text-emerald-300' : 'text-zinc-100'}">{summary.hosts_enforcing}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">{summary.hosts_observing} observing only{summary.hosts_blocked > 0 ? ` · ${summary.hosts_blocked} with errors` : ''}</div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Active bans</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{summary.active_local}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">{summary.bans_24h} issued in the last 24 h</div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Fleet blocklist</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{summary.fleet_size}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">{summary.fleet_bans_24h} added in the last 24 h</div>
        </div>
      </div>
      {#if settings && !settings.enforce && summary.hosts_detecting > 0}
        <div class="px-4 sm:px-5 py-3 border-t border-sky-900/40 bg-sky-950/20 text-xs text-sky-100/90">
          Detection is running in observe mode: bans are recorded so you can review what would be blocked, but nothing is enforced yet. Add your own address to the allowlist below, then turn enforcement on.
        </div>
      {/if}
    </section>

    <section class="mt-4 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <header class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-3 border-b border-zinc-800">
        <div>
          <div class="text-xs uppercase tracking-wider text-zinc-500">Ban activity</div>
          <p class="mt-0.5 text-[11px] text-zinc-500">Every address the fleet decided to ban, whether or not enforcement applied it.</p>
        </div>
        <div class="flex items-center gap-1">
          {#each statsWindows as w (w.value)}
            <button
              type="button"
              onclick={() => setStatsWindow(w.value)}
              class="rounded-md border px-2 py-1 text-[11px] {statsWindow === w.value
                ? 'border-sky-500/40 bg-sky-500/10 text-sky-300'
                : 'border-zinc-800 text-zinc-400 hover:text-zinc-200'}">{w.label}</button>
          {/each}
        </div>
      </header>
      {#if stats === null}
        <div class="p-4"><div class="h-40 rounded-lg shimmer"></div></div>
      {:else}
        <div class="grid grid-cols-2 lg:grid-cols-4 divide-y lg:divide-y-0 divide-zinc-800 lg:divide-x border-b border-zinc-800">
          <div class="px-4 sm:px-5 py-3">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500">Bans issued</div>
            <div class="mt-1 text-2xl font-semibold text-zinc-100 numeric">{stats.total_bans}</div>
            <div class="text-[11px] text-zinc-500 mt-0.5">{stats.enforced_bans} actually blocked</div>
          </div>
          <div class="px-4 sm:px-5 py-3">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500">Distinct addresses</div>
            <div class="mt-1 text-2xl font-semibold text-zinc-100 numeric">{stats.distinct_ips}</div>
            <div class="text-[11px] text-zinc-500 mt-0.5">across {stats.hosts_seen} host{stats.hosts_seen === 1 ? '' : 's'}</div>
          </div>
          <div class="px-4 sm:px-5 py-3">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500">Repeat offenders</div>
            <div class="mt-1 text-2xl font-semibold numeric {stats.repeat_ips > 0 ? 'text-amber-300' : 'text-zinc-100'}">{stats.repeat_ips}</div>
            <div class="text-[11px] text-zinc-500 mt-0.5">banned more than once</div>
          </div>
          <div class="px-4 sm:px-5 py-3">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500">One-off addresses</div>
            <div class="mt-1 text-2xl font-semibold text-zinc-100 numeric">{onceOnlyIPs}</div>
            <div class="text-[11px] text-zinc-500 mt-0.5">seen once in {windowLabel(statsWindow)}</div>
          </div>
        </div>
        <div class="px-3 py-3">
          <MultiChart
            series={banSeries}
            fromMs={statsFromMs}
            toMs={statsToMs}
            height={180}
            bars
            yClampMin={0}
            yMinSpan={4}
            format={(v, e = 0) => v.toFixed(e)}
            emptyText="No bans in this range" />
        </div>
        {#if stats.window_clipped}
          <div class="px-4 sm:px-5 pb-3 text-[11px] text-zinc-500">
            History only reaches back {Math.round(stats.retention_s / 86400)} days, so this window is not fully populated. Raise RETENTION_IPBAN_EVENTS to keep more.
          </div>
        {/if}
        <div class="border-t border-zinc-800">
          <div class="px-4 sm:px-5 py-2.5 text-[11px] uppercase tracking-wider text-zinc-500">Top offenders</div>
          {#if stats.offenders.length === 0}
            <div class="px-5 pb-5 text-sm text-zinc-500">No bans recorded in this window.</div>
          {:else}
            <div class="overflow-x-auto">
              <table class="w-full text-sm">
                <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
                  <tr>
                    <th class="text-left font-medium px-5 py-2.5">Address</th>
                    <th class="text-right font-medium px-3 py-2.5">Bans</th>
                    <th class="text-right font-medium px-3 py-2.5 hidden sm:table-cell">Hosts</th>
                    <th class="text-left font-medium px-3 py-2.5 hidden md:table-cell">Last user</th>
                    <th class="text-left font-medium px-3 py-2.5 hidden sm:table-cell">First seen</th>
                    <th class="text-left font-medium px-3 py-2.5">Last seen</th>
                    <th class="text-right font-medium px-5 py-2.5"></th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-zinc-800/70">
                  {#each stats.offenders as o (o.ip)}
                    <tr class="hover:bg-zinc-900/60">
                      <td class="px-5 py-2.5 font-mono text-zinc-200">
                        {o.ip}
                        {#if o.fleet}<span class="ml-2 rounded border border-sky-500/30 bg-sky-500/10 px-1 py-px text-[10px] uppercase tracking-wider text-sky-300">fleet</span>{/if}
                        {#if o.active}<span class="ml-2 rounded border border-zinc-700 bg-zinc-800/60 px-1 py-px text-[10px] uppercase tracking-wider text-zinc-400">active</span>{/if}
                      </td>
                      <td class="px-3 py-2.5 text-right numeric {o.bans > 1 ? 'text-amber-300' : 'text-zinc-300'}">{o.bans}</td>
                      <td class="px-3 py-2.5 text-right numeric text-zinc-300 hidden sm:table-cell">{o.hosts}</td>
                      <td class="px-3 py-2.5 font-mono text-xs text-zinc-400 hidden md:table-cell">{o.user || '—'}</td>
                      <td class="px-3 py-2.5 text-zinc-400 numeric hidden sm:table-cell" title={absTime(o.first_seen)}>{timeAgo(o.first_seen)}</td>
                      <td class="px-3 py-2.5 text-zinc-400 numeric" title={absTime(o.last_seen)}>{timeAgo(o.last_seen)}</td>
                      <td class="px-5 py-2.5 text-right">
                        {#if o.fleet}
                          <span class="text-[11px] text-zinc-600">on blocklist</span>
                        {:else}
                          <button
                            type="button"
                            onclick={() => banOffender(o.ip)}
                            class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Ban fleet-wide</button>
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </div>
      {/if}
    </section>

    {#if attention.length > 0}
      <section class="mt-4 rounded-xl border border-amber-900/50 bg-amber-950/20 overflow-hidden">
        <header class="px-4 sm:px-5 py-2.5 border-b border-amber-900/40 text-xs uppercase tracking-wider text-amber-300">Needs attention</header>
        <ul class="divide-y divide-amber-900/30">
          {#each attention as h (h.host_id)}
            <li class="px-4 sm:px-5 py-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              <a href="/hosts/{h.host_id}?tab=security" class="font-mono text-zinc-100 hover:text-white">{h.hostname}</a>
              <span class="text-xs text-amber-100/80 min-w-0 break-words">{attentionText(h)}</span>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <div class="mt-6 space-y-6">
      <section>
        <div class="flex items-baseline justify-between gap-3 flex-wrap">
          <div>
            <h2 class="text-sm font-medium text-zinc-100">Fleet blocklist</h2>
            <p class="mt-0.5 text-xs text-zinc-500">Addresses every enforcing host blocks. Entries arrive automatically once an attacker is banned on {settings?.fleet_min_hosts ?? 2} hosts or {settings?.fleet_min_bans ?? 3} times within a week, and expire after {settings ? Math.round(settings.fleet_ttl_s / 3600) : 24} hours without new evidence.</p>
          </div>
        </div>
        <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
          <form
            class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-end gap-3"
            onsubmit={(e) => {
              e.preventDefault();
              banError = null;
              if (!banIP.trim()) {
                banError = 'Enter an IP address to ban.';
                return;
              }
              confirmManualBan = true;
            }}>
            <div class="min-w-[12rem] flex-1">
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1" for="ban-ip">Ban an address fleet-wide</label>
              <input id="ban-ip" bind:value={banIP} placeholder="203.0.113.5" class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1" for="ban-ttl">For</label>
              <select id="ban-ttl" bind:value={banTTL} class="rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
                <option value="3600">1 hour</option>
                <option value="86400">24 hours</option>
                <option value="604800">7 days</option>
                <option value="2592000">30 days</option>
              </select>
            </div>
            <div class="min-w-[12rem] flex-1">
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1" for="ban-note">Note</label>
              <input id="ban-note" bind:value={banNote} maxlength="200" placeholder="optional" class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm" />
            </div>
            <button type="submit" disabled={banBusy} class="text-sm px-4 py-2 rounded-md bg-rose-500/20 border border-rose-500/40 text-rose-200 hover:bg-rose-500/30 disabled:opacity-50">Ban</button>
            {#if banError}
              <div class="basis-full text-xs text-rose-300">{banError}</div>
            {/if}
          </form>
          {#if fleet.length === 0}
            <div class="px-5 py-6 text-sm text-zinc-500">The fleet blocklist is empty. It fills as hosts report repeat offenders, or when you ban an address above.</div>
          {:else}
            <div class="hidden md:block overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                    <th class="px-4 py-2.5 font-medium">Address</th>
                    <th class="px-4 py-2.5 font-medium text-right">Hosts</th>
                    <th class="px-4 py-2.5 font-medium text-right">Bans</th>
                    <th class="px-4 py-2.5 font-medium">First seen</th>
                    <th class="px-4 py-2.5 font-medium">Last seen</th>
                    <th class="px-4 py-2.5 font-medium">Expires</th>
                    <th class="px-4 py-2.5 font-medium">Source</th>
                    <th class="px-4 py-2.5 font-medium text-right"></th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-zinc-800/70">
                  {#each fleet as f (f.ip)}
                    <tr class="hover:bg-zinc-900/60">
                      <td class="px-4 py-2.5 font-mono text-zinc-200">{f.ip}</td>
                      <td class="px-4 py-2.5 text-right numeric text-zinc-300" title="{f.active_hosts} host(s) currently hold a local ban">{f.host_count}</td>
                      <td class="px-4 py-2.5 text-right numeric text-zinc-300">{f.ban_count}</td>
                      <td class="px-4 py-2.5 text-zinc-400 numeric" title={absTime(f.first_seen)}>{timeAgo(f.first_seen)}</td>
                      <td class="px-4 py-2.5 text-zinc-400 numeric" title={absTime(f.last_seen)}>{timeAgo(f.last_seen)}</td>
                      <td class="px-4 py-2.5 text-zinc-400 numeric" title={absTime(f.expires_at)}>{timeUntil(f.expires_at)}</td>
                      <td class="px-4 py-2.5">
                        <span class="rounded-md border px-1.5 py-0.5 text-[10px] uppercase tracking-wider {f.source === 'manual' ? tonePill.warn : tonePill.none}">{f.source}</span>
                        {#if f.note}<span class="ml-2 text-xs text-zinc-500">{f.note}</span>{/if}
                        {#if f.created_by}<span class="ml-1 text-[11px] text-zinc-600">by {f.created_by}</span>{/if}
                      </td>
                      <td class="px-4 py-2.5 text-right">
                        <button type="button" onclick={() => (toFleetUnban = f)} class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Unban</button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
            <div class="md:hidden divide-y divide-zinc-800/70">
              {#each fleet as f (f.ip)}
                <div class="px-4 py-3 space-y-1.5">
                  <div class="flex items-center justify-between gap-3">
                    <span class="font-mono text-sm text-zinc-200">{f.ip}</span>
                    <button type="button" onclick={() => (toFleetUnban = f)} class="shrink-0 text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Unban</button>
                  </div>
                  <div class="text-xs text-zinc-500 numeric">{f.host_count} hosts · {f.ban_count} bans · expires {timeUntil(f.expires_at)} · {f.source}{f.note ? ` · ${f.note}` : ''}</div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </section>

      <section>
        <div>
          <h2 class="text-sm font-medium text-zinc-100">Hosts</h2>
          <p class="mt-0.5 text-xs text-zinc-500">What each agent is doing right now. Policy columns override the fleet defaults per host; "inherit" follows the settings below.</p>
        </div>
        {#if policyError}
          <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{policyError}</div>
        {/if}
        <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
          {#if liveHosts.length === 0}
            <div class="px-5 py-6 text-sm text-zinc-500">No hosts registered yet.</div>
          {:else}
            <div class="hidden md:block overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                    <th class="px-4 py-2.5 font-medium">Host</th>
                    <th class="px-4 py-2.5 font-medium">Detection</th>
                    <th class="px-4 py-2.5 font-medium">Enforcement</th>
                    <th class="px-4 py-2.5 font-medium">Share bans</th>
                    <th class="px-4 py-2.5 font-medium">Apply fleet list</th>
                    <th class="px-4 py-2.5 font-medium text-right">Active</th>
                    <th class="px-4 py-2.5 font-medium">Reported</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-zinc-800/70">
                  {#each liveHosts as h (h.host_id)}
                    {@const d = detectInfo(h)}
                    {@const en = enforceInfo(h)}
                    {@const linux = h.os.toLowerCase().includes('linux')}
                    <tr class="hover:bg-zinc-900/60 {policyBusy === h.host_id ? 'opacity-60' : ''}">
                      <td class="px-4 py-2.5">
                        <div class="flex items-center gap-2">
                          <StatusDot status={statusFor(h.last_seen ?? undefined, h.sample_interval_s || 10)} size="sm" inline />
                          <a href="/hosts/{h.host_id}?tab=security" class="font-mono text-zinc-200 hover:text-zinc-100">{h.hostname}</a>
                        </div>
                      </td>
                      <td class="px-4 py-2.5">
                        <div class="flex items-center gap-2">
                          <span class="h-1.5 w-1.5 rounded-full {toneDot[d.tone]}"></span>
                          <span class="text-xs {toneText[d.tone]}">{d.label}</span>
                          {#if linux}
                            <select class={selectClass} value={selValue(h.override.detect)} onchange={(e) => setPolicy(h, 'detect', e)} aria-label="Detection policy for {h.hostname}">
                              <option value="inherit">inherit ({settings?.enabled ? 'on' : 'off'})</option>
                              <option value="on">on</option>
                              <option value="off">off</option>
                            </select>
                          {/if}
                        </div>
                      </td>
                      <td class="px-4 py-2.5">
                        <div class="flex items-center gap-2">
                          <span class="h-1.5 w-1.5 rounded-full {toneDot[en.tone]}"></span>
                          <span class="text-xs {toneText[en.tone]}">{en.label}</span>
                          {#if linux}
                            <select class={selectClass} value={selValue(h.override.enforce)} onchange={(e) => setPolicy(h, 'enforce', e)} aria-label="Enforcement policy for {h.hostname}">
                              <option value="inherit">inherit ({settings?.enforce ? 'on' : 'off'})</option>
                              <option value="on">on</option>
                              <option value="off">off</option>
                            </select>
                          {/if}
                        </div>
                      </td>
                      <td class="px-4 py-2.5">
                        {#if linux}
                          <select class={selectClass} value={selValue(h.override.contribute)} onchange={(e) => setPolicy(h, 'contribute', e)} aria-label="Share-bans policy for {h.hostname}">
                            <option value="inherit">inherit ({settings?.contribute ? 'on' : 'off'})</option>
                            <option value="on">on</option>
                            <option value="off">off</option>
                          </select>
                        {:else}
                          <span class="text-zinc-600">—</span>
                        {/if}
                      </td>
                      <td class="px-4 py-2.5">
                        {#if linux}
                          <select class={selectClass} value={selValue(h.override.apply_fleet)} onchange={(e) => setPolicy(h, 'apply_fleet', e)} aria-label="Apply-fleet-list policy for {h.hostname}">
                            <option value="inherit">inherit ({settings?.apply_fleet ? 'on' : 'off'})</option>
                            <option value="on">on</option>
                            <option value="off">off</option>
                          </select>
                        {:else}
                          <span class="text-zinc-600">—</span>
                        {/if}
                      </td>
                      <td class="px-4 py-2.5 text-right numeric text-zinc-300">{h.active_local}{#if h.fleet_applied > 0}<span class="text-zinc-600"> +{h.fleet_applied} fleet</span>{/if}</td>
                      <td class="px-4 py-2.5 text-zinc-500 text-xs numeric">{h.reported_at ? timeAgo(h.reported_at) : '—'}{#if h.config_stale}<span class="ml-2 text-amber-300">stale policy</span>{/if}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
            <div class="md:hidden divide-y divide-zinc-800/70">
              {#each liveHosts as h (h.host_id)}
                {@const d = detectInfo(h)}
                {@const en = enforceInfo(h)}
                {@const linux = h.os.toLowerCase().includes('linux')}
                <div class="px-4 py-3 space-y-2">
                  <div class="flex items-center justify-between gap-3">
                    <a href="/hosts/{h.host_id}?tab=security" class="min-w-0 truncate font-mono text-sm text-zinc-200 hover:text-zinc-100">{h.hostname}</a>
                    <span class="text-xs text-zinc-500 numeric">{h.active_local} active</span>
                  </div>
                  <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                    <span class="{toneText[d.tone]}">{d.label}</span>
                    <span class="{toneText[en.tone]}">{en.label}</span>
                  </div>
                  {#if linux}
                    <div class="grid grid-cols-2 gap-2 text-xs">
                      <label class="flex flex-col gap-1 text-zinc-500">Detection
                        <select class={selectClass} value={selValue(h.override.detect)} onchange={(e) => setPolicy(h, 'detect', e)}>
                          <option value="inherit">inherit</option><option value="on">on</option><option value="off">off</option>
                        </select>
                      </label>
                      <label class="flex flex-col gap-1 text-zinc-500">Enforcement
                        <select class={selectClass} value={selValue(h.override.enforce)} onchange={(e) => setPolicy(h, 'enforce', e)}>
                          <option value="inherit">inherit</option><option value="on">on</option><option value="off">off</option>
                        </select>
                      </label>
                      <label class="flex flex-col gap-1 text-zinc-500">Share bans
                        <select class={selectClass} value={selValue(h.override.contribute)} onchange={(e) => setPolicy(h, 'contribute', e)}>
                          <option value="inherit">inherit</option><option value="on">on</option><option value="off">off</option>
                        </select>
                      </label>
                      <label class="flex flex-col gap-1 text-zinc-500">Apply fleet list
                        <select class={selectClass} value={selValue(h.override.apply_fleet)} onchange={(e) => setPolicy(h, 'apply_fleet', e)}>
                          <option value="inherit">inherit</option><option value="on">on</option><option value="off">off</option>
                        </select>
                      </label>
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </section>

      <section>
        <div>
          <h2 class="text-sm font-medium text-zinc-100">Settings</h2>
          <p class="mt-0.5 text-xs text-zinc-500">Fleet-wide policy. Every agent applies it within a minute of saving; per-host overrides above take precedence.</p>
        </div>
        <form onsubmit={saveSettings} class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
          <div class="px-4 sm:px-5 py-4 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-3">
            <label class="flex items-start gap-2 text-sm text-zinc-300 cursor-pointer select-none">
              <input type="checkbox" bind:checked={form.enabled} onchange={markDirty} class="mt-1 accent-emerald-500" />
              <span><span class="text-zinc-100">Detection</span> <span class="text-zinc-500 text-xs">— agents watch sshd and count failures per address</span></span>
            </label>
            <label class="flex items-start gap-2 text-sm cursor-pointer select-none {enforceBlocked ? 'text-zinc-500' : 'text-zinc-300'}">
              <input type="checkbox" bind:checked={form.enforce} onchange={markDirty} disabled={enforceBlocked && !form.enforce} class="mt-1 accent-amber-500" />
              <span><span class={enforceBlocked ? 'text-zinc-400' : 'text-zinc-100'}>Enforcement</span> <span class="text-zinc-500 text-xs">— banned addresses are dropped by nftables on each capable host{enforceBlocked ? '; needs a non-empty allowlist first' : ''}</span></span>
            </label>
            <label class="flex items-start gap-2 text-sm text-zinc-300 cursor-pointer select-none">
              <input type="checkbox" bind:checked={form.contribute} onchange={markDirty} class="mt-1 accent-emerald-500" />
              <span><span class="text-zinc-100">Share bans</span> <span class="text-zinc-500 text-xs">— local bans count towards the fleet blocklist</span></span>
            </label>
            <label class="flex items-start gap-2 text-sm text-zinc-300 cursor-pointer select-none">
              <input type="checkbox" bind:checked={form.apply_fleet} onchange={markDirty} class="mt-1 accent-emerald-500" />
              <span><span class="text-zinc-100">Apply fleet list</span> <span class="text-zinc-500 text-xs">— enforcing hosts also drop the fleet blocklist</span></span>
            </label>
          </div>
          <div class="px-4 sm:px-5 py-4 border-t border-zinc-800 grid grid-cols-2 lg:grid-cols-4 gap-4">
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-retry">Failures before a ban</label>
              <input id="s-retry" type="number" min="1" max="100" bind:value={form.max_retry} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-find">Within (minutes)</label>
              <input id="s-find" type="number" min="1" max="1440" bind:value={form.find_time_min} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-ban">Ban for (minutes)</label>
              <input id="s-ban" type="number" min="1" max="43200" bind:value={form.ban_time_min} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-banmax">Max ban, repeat offenders (hours)</label>
              <input id="s-banmax" type="number" min="1" max="8760" bind:value={form.ban_time_max_h} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-hosts">Fleet ban after N hosts</label>
              <input id="s-hosts" type="number" min="1" max="1000" bind:value={form.fleet_min_hosts} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-bans">or after N bans in a week</label>
              <input id="s-bans" type="number" min="1" max="10000" bind:value={form.fleet_min_bans} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-ttl">Fleet entry lifetime (hours)</label>
              <input id="s-ttl" type="number" min="1" max="720" bind:value={form.fleet_ttl_h} oninput={markDirty} class={inputClass} />
            </div>
            <div>
              <label class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5" for="s-mode">Pattern set</label>
              <select id="s-mode" bind:value={form.mode} onchange={markDirty} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
                <option value="normal">normal — failed logins only</option>
                <option value="aggressive">aggressive — also scanners and protocol probes</option>
              </select>
            </div>
          </div>
          <div class="px-4 sm:px-5 py-4 border-t border-zinc-800 grid grid-cols-1 lg:grid-cols-2 gap-4">
            <div>
              <div class="flex items-center justify-between mb-1.5">
                <label class="text-[11px] uppercase tracking-wider text-zinc-500" for="s-allow">Allowlist (never banned, one per line)</label>
                {#if clientIP}
                  <button type="button" onclick={addMyIP} disabled={clientIPListed} class="text-[11px] px-2 py-0.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
                    {clientIPListed ? `your IP ${clientIP} is listed` : `add my IP ${clientIP}`}
                  </button>
                {/if}
              </div>
              <textarea id="s-allow" rows="5" bind:value={form.allowlist} oninput={markDirty} placeholder={'203.0.113.7\n198.51.100.0/24\n2001:db8::/48'} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono"></textarea>
              <p class="mt-1.5 text-[11px] text-zinc-500">Put your own addresses, your office and your VPN here before enforcing anything: an admin with several SSH keys can trip the threshold exactly like an attacker. Loopback, link-local and each agent's own addresses are always protected; the server's address is protected too.</p>
            </div>
            <div class="space-y-3">
              <label class="flex items-start gap-2 text-sm text-zinc-300 cursor-pointer select-none">
                <input type="checkbox" bind:checked={form.ban_private} onchange={markDirty} class="mt-1 accent-amber-500" />
                <span><span class="text-zinc-100">Also ban private addresses</span> <span class="text-zinc-500 text-xs">— RFC 1918, CGNAT and ULA ranges become bannable locally. They never propagate to the fleet. Off by default because a mistyped LAN password is the most common way to lock yourself out.</span></span>
              </label>
              <p class="text-[11px] text-zinc-500">Repeat offenders escalate: each new ban within a week doubles the previous one, up to the maximum above. Unbanning an address resets its counter.</p>
            </div>
          </div>
          <div class="px-4 sm:px-5 py-3 border-t border-zinc-800 flex flex-wrap items-center gap-3">
            <button type="submit" disabled={saving || !formDirty} class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50">
              {saving ? 'Saving…' : 'Save settings'}
            </button>
            {#if formDirty && settings}
              <button type="button" onclick={() => hydrate(settings!)} class="text-xs px-2.5 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Discard changes</button>
            {/if}
            {#if saveError}<span class="text-xs text-rose-300">{saveError}</span>{/if}
            {#if saveInfo}<span class="text-xs text-emerald-300">{saveInfo}</span>{/if}
            {#if settings}<span class="ml-auto text-[11px] text-zinc-600 numeric">policy v{settings.version} · updated {timeAgo(settings.updated_at)}</span>{/if}
          </div>
        </form>
      </section>

      <section>
        <div class="flex flex-wrap items-end justify-between gap-2">
          <div>
            <h2 class="text-sm font-medium text-zinc-100">Recent activity</h2>
            <p class="mt-0.5 text-xs text-zinc-500">Bans, unbans and fleet changes across all hosts.</p>
          </div>
          <a
            href={api.ipbanEventsCsvUrl()}
            download
            class="inline-flex items-center gap-1.5 rounded-md border border-zinc-800 px-2.5 py-1.5 text-[11px] text-zinc-300 hover:text-zinc-100 hover:border-zinc-700"
            title="Download the full ban event history as CSV">
            <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
              <path d="M6 1.5v6m0 0L3.75 5.25M6 7.5l2.25-2.25M2 9.5h8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <span>CSV</span>
          </a>
        </div>
        <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
          {#if events.length === 0}
            <div class="px-5 py-6 text-sm text-zinc-500">Nothing recorded yet.</div>
          {:else}
            <ul class="divide-y divide-zinc-800/70">
              {#each pagedEvents as ev (ev.id)}
                {@const a = actionLabel(ev.action, ev.enforced)}
                <li class="px-4 sm:px-5 py-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
                  <span class="text-[11px] text-zinc-500 numeric w-24 shrink-0" title={absTime(ev.time)}>{timeAgo(ev.time)}</span>
                  <span class="rounded-md border px-1.5 py-0.5 text-[10px] uppercase tracking-wider {tonePill[a.tone]}">{a.text}</span>
                  <span class="font-mono text-zinc-200">{ev.ip}</span>
                  {#if ev.host_id}
                    <a href="/hosts/{ev.host_id}?tab=security" class="font-mono text-xs text-zinc-400 hover:text-zinc-200">{ev.hostname || `host ${ev.host_id}`}</a>
                  {:else}
                    <span class="text-xs text-zinc-500">fleet</span>
                  {/if}
                  <span class="text-xs text-zinc-500">
                    {#if ev.action === 'ban'}
                      {ev.failures} failures{ev.user ? ` · last user ${ev.user}` : ''}
                    {:else if ev.actor}
                      by {ev.actor}
                    {/if}
                    {#if ev.note}<span class="text-zinc-600"> · {ev.note}</span>{/if}
                  </span>
                </li>
              {/each}
            </ul>
            {#if events.length > evPageSizes[0]}
              <div class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-2.5 border-t border-zinc-800">
                <div class="flex items-center gap-2">
                  <div class="text-[10px] uppercase tracking-wider text-zinc-500">Rows</div>
                  <div class="flex items-center gap-0.5">
                    {#each evPageSizes as size (size)}
                      <button
                        type="button"
                        onclick={() => setEvPageSize(size)}
                        aria-pressed={evPageSize === size}
                        class="px-2 py-1 rounded-md text-xs font-medium numeric transition-colors {evPageSize === size ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">{size}</button>
                    {/each}
                  </div>
                </div>
                <div class="flex items-center gap-2">
                  <div class="text-xs text-zinc-500 numeric">{evFrom + 1}–{evTo} of {events.length} · page {evPageIndex + 1} of {evPageCount}</div>
                  <button
                    type="button"
                    onclick={() => gotoEvPage(evPageIndex - 1)}
                    disabled={evPageIndex === 0}
                    aria-label="Newer events"
                    title="Newer"
                    class="inline-flex items-center rounded-md border border-zinc-800 px-2 py-1.5 text-zinc-400 transition-colors hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-35 disabled:hover:text-zinc-400 disabled:hover:bg-transparent">
                    <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
                      <path d="M8 2.25 4.25 6 8 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                    </svg>
                  </button>
                  <button
                    type="button"
                    onclick={() => gotoEvPage(evPageIndex + 1)}
                    disabled={evPageIndex >= evPageCount - 1}
                    aria-label="Older events"
                    title="Older"
                    class="inline-flex items-center rounded-md border border-zinc-800 px-2 py-1.5 text-zinc-400 transition-colors hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-35 disabled:hover:text-zinc-400 disabled:hover:bg-transparent">
                    <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
                      <path d="M4 2.25 7.75 6 4 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                    </svg>
                  </button>
                </div>
              </div>
            {/if}
          {/if}
        </div>
      </section>
    </div>
  {/if}
</div>

{#snippet manualBanBody()}
  <p class="text-sm text-zinc-300">
    Add <span class="font-mono text-zinc-100">{banIP.trim()}</span> to the fleet blocklist for {ttlLabel(banTTL)}. Every enforcing host that applies the fleet list drops its traffic within about a minute.
  </p>
  {#if !settings?.enforce}
    <p class="mt-2 text-xs text-amber-200/90">Enforcement is currently off fleet-wide, so the entry is recorded but no host blocks it until enforcement is turned on.</p>
  {/if}
{/snippet}

<ConfirmDialog
  open={confirmManualBan}
  title="Ban address fleet-wide"
  body={manualBanBody}
  confirmLabel="Ban"
  danger
  onconfirm={doManualBan}
  onclose={() => (confirmManualBan = false)}
/>

{#snippet fleetUnbanBody()}
  {#if toFleetUnban}
    <p class="text-sm text-zinc-300">
      Remove <span class="font-mono text-zinc-100">{toFleetUnban.ip}</span> from the fleet blocklist and lift its local bans on {toFleetUnban.active_hosts} host{toFleetUnban.active_hosts === 1 ? '' : 's'}. It will not be re-added automatically for {settings ? Math.round(settings.fleet_ttl_s / 3600) : 24} hours, even if failures continue.
    </p>
  {/if}
{/snippet}

<ConfirmDialog
  open={toFleetUnban !== null}
  title="Lift fleet ban"
  body={fleetUnbanBody}
  confirmLabel="Unban"
  onconfirm={doFleetUnban}
  onclose={() => (toFleetUnban = null)}
/>
