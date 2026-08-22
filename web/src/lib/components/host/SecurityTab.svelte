<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type CollectorStatus, type IPBanHost, type IPBanActive, type IPBanEvent, type IPBanStats, type SeriesPoint, type SeriesEntry } from '$lib/api';
  import { timeAgo, timeUntil } from '$lib/format';
  import { announcer } from '$lib/announce.svelte';
  import { rangeToFrom, rangeToTo, rangeBoundsMs, rangeEquals, type Range } from '$lib/time';
  import { groupPoints, groupSeries } from '$lib/series';
  import MultiChart from '$lib/components/MultiChart.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let {
    hostId,
    range,
    collectorStatus = {},
    os = ''
  }: {
    hostId: number;
    range: Range;
    sampleIntervalS?: number;
    collectorStatus?: Record<string, CollectorStatus>;
    os?: string;
  } = $props();

  type Tone = 'good' | 'warn' | 'bad' | 'info' | 'none';
  const toneText: Record<Tone, string> = {
    good: 'text-emerald-300',
    warn: 'text-amber-300',
    bad: 'text-rose-300',
    info: 'text-sky-300',
    none: 'text-zinc-400'
  };
  const tonePill: Record<Tone, string> = {
    good: 'border-emerald-900/60 bg-emerald-950/40 text-emerald-300',
    warn: 'border-amber-900/60 bg-amber-950/40 text-amber-300',
    bad: 'border-rose-900/60 bg-rose-950/40 text-rose-300',
    info: 'border-sky-900/60 bg-sky-950/40 text-sky-300',
    none: 'border-zinc-800 bg-zinc-950 text-zinc-500'
  };

  const isLinux = $derived((os ?? '').toLowerCase().includes('linux'));
  const collectorState = $derived(collectorStatus.ipban?.state ?? '');

  let view = $state<IPBanHost | null>(null);
  let active = $state<IPBanActive[]>([]);
  let events = $state<IPBanEvent[]>([]);
  let stats = $state<IPBanStats | null>(null);
  const offenderWindow = '168h';
  let loading = $state(true);
  let error = $state<string | null>(null);
  let baseUrl = $state(typeof window !== 'undefined' ? window.location.origin : '');
  let copied = $state<'idle' | 'done' | 'failed'>('idle');
  let copyTimer: ReturnType<typeof setTimeout> | null = null;

  let failures = $state<SeriesEntry[]>([]);
  let bans = $state<SeriesPoint[]>([]);
  let activeSeries = $state<SeriesPoint[]>([]);
  let fromMs = $state(0);
  let toMs = $state(0);
  let chartsLoading = $state(true);
  let chartZoom = $state<{ fromMs: number; toMs: number } | null>(null);
  let prevRange: Range | null = null;

  let refreshGen = 0;
  let inflight: AbortController | null = null;
  let chartGen = 0;
  let chartInflight: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;

  let toUnban = $state<IPBanActive | null>(null);

  const evPageSizes = [8, 16, 50];
  let evPage = $state(0);
  let evPageSize = $state(evPageSizes[0]);
  const evPageCount = $derived(Math.max(1, Math.ceil(events.length / evPageSize)));
  const evPageIndex = $derived(Math.min(Math.max(evPage, 0), evPageCount - 1));
  const evFrom = $derived(evPageIndex * evPageSize);
  const evTo = $derived(Math.min(evFrom + evPageSize, events.length));
  const pagedEvents = $derived(events.slice(evFrom, evTo));

  function gotoEvPage(page: number) {
    evPage = Math.min(Math.max(page, 0), evPageCount - 1);
  }

  function setEvPageSize(size: number) {
    evPageSize = size;
    evPage = 0;
  }

  const enableSnippet = $derived(
    `SM_ENABLE_IPBAN=1 sudo --preserve-env=SM_ENABLE_IPBAN bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`
  );

  async function loadServerInfo() {
    try {
      const info = await api.serverInfo();
      baseUrl = (info.url || window.location.origin).replace(/\/+$/, '');
    } catch {
      baseUrl = window.location.origin;
    }
  }

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    try {
      const [hosts, bansNow, recent, stt] = await Promise.all([
        api.ipbanHosts({ signal: ac.signal }),
        api.ipbanActive({ host: hostId, limit: 500, signal: ac.signal }),
        api.ipbanEvents({ host: hostId, limit: 50, signal: ac.signal }),
        api.ipbanStats({ host: hostId, window: offenderWindow, top: 10, signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      view = hosts.find((h) => h.host_id === hostId) ?? null;
      active = bansNow;
      events = recent;
      stats = stt;
      error = null;
    } catch (e) {
      if (gen !== refreshGen || (e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      if (gen === refreshGen) loading = false;
    }
  }

  async function refreshCharts() {
    const gen = ++chartGen;
    chartInflight?.abort();
    const ac = new AbortController();
    chartInflight = ac;
    let from: string;
    let to: string | undefined;
    const zoomed = chartZoom;
    if (zoomed) {
      from = new Date(zoomed.fromMs).toISOString();
      to = new Date(zoomed.toMs).toISOString();
    } else {
      const b = rangeBoundsMs(range);
      from = rangeToFrom(range);
      to = rangeToTo(range);
      fromMs = b.fromMs;
      toMs = b.toMs;
    }
    try {
      const resp = await api.seriesGroup({
        host: hostId,
        series: [
          { metric: 'ipban_auth_failures_per_min', splitBy: 'source' },
          { metric: 'ipban_bans_per_hour' },
          { metric: 'ipban_active_local' }
        ],
        from,
        to,
        signal: ac.signal
      });
      if (gen !== chartGen) return;
      failures = groupSeries(resp, 'ipban_auth_failures_per_min');
      bans = groupPoints(resp, 'ipban_bans_per_hour');
      activeSeries = groupPoints(resp, 'ipban_active_local');
      chartsLoading = false;
    } catch (e) {
      if (gen !== chartGen || (e as { name?: string })?.name === 'AbortError') return;
      chartsLoading = false;
    }
  }

  function handleZoom(f: number, t: number) {
    chartZoom = { fromMs: f, toMs: t };
    fromMs = f;
    toMs = t;
    void refreshCharts();
  }

  function handleReset() {
    chartZoom = null;
    const b = rangeBoundsMs(range);
    fromMs = b.fromMs;
    toMs = b.toMs;
    void refreshCharts();
  }

  $effect(() => {
    const current = range;
    if (prevRange !== null && !rangeEquals(current, prevRange)) {
      chartZoom = null;
      chartsLoading = true;
    }
    prevRange = current;
    untrack(() => void refreshCharts());
  });

  onMount(() => {
    void loadServerInfo();
    void refresh();
    timer = setInterval(() => {
      void refresh();
      if (chartZoom === null) void refreshCharts();
    }, 15_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (copyTimer) clearTimeout(copyTimer);
    inflight?.abort();
    chartInflight?.abort();
  });

  async function copySnippet() {
    if (copyTimer) clearTimeout(copyTimer);
    try {
      await navigator.clipboard.writeText(enableSnippet);
      copied = 'done';
      announcer.say('Command copied to clipboard');
    } catch {
      copied = 'failed';
      announcer.say('Could not copy the command to the clipboard');
    }
    copyTimer = setTimeout(() => (copied = 'idle'), 1500);
  }

  async function doUnban() {
    if (!toUnban) return;
    const ip = toUnban.ip;
    await api.ipbanHostUnban(hostId, ip);
    announcer.say(`Unban of ${ip} sent to the agent`);
    toUnban = null;
    setTimeout(() => void refresh(), 1500);
    setTimeout(() => void refresh(), 12_000);
  }

  function detectInfo(h: IPBanHost | null): { label: string; tone: Tone } {
    if (!isLinux) return { label: 'Linux agents only', tone: 'none' };
    if (h && !h.supported) return { label: 'agent too old', tone: 'none' };
    if (!h || h.detect_state === '') return { label: collectorState === '' ? 'no report yet' : collectorState, tone: 'none' };
    switch (h.detect_state) {
      case 'ok':
        return { label: 'watching sshd', tone: 'good' };
      case 'no_permission':
        return { label: 'no log access', tone: 'warn' };
      case 'unavailable':
        return { label: 'no auth log found', tone: 'warn' };
      case 'error':
        return { label: 'error', tone: 'bad' };
      case 'disabled':
        return { label: 'off', tone: 'none' };
      case 'pending':
        return { label: 'waiting for policy', tone: 'none' };
      case 'starting':
        return { label: 'starting', tone: 'none' };
      default:
        return { label: h.detect_state, tone: 'none' };
    }
  }

  function enforceInfo(h: IPBanHost | null): { label: string; tone: Tone } {
    if (!isLinux) return { label: 'unsupported', tone: 'none' };
    if (h && !h.supported) return { label: 'agent too old', tone: 'none' };
    if (!h || h.enforce_state === '') return { label: 'no report yet', tone: 'none' };
    if (!h.effective.enforce) return { label: 'observing only', tone: 'info' };
    switch (h.enforce_state) {
      case 'ok':
        return { label: 'enforcing', tone: 'good' };
      case 'no_permission':
        return { label: 'needs CAP_NET_ADMIN', tone: 'warn' };
      case 'unsupported':
        return { label: 'no nftables', tone: 'warn' };
      case 'error':
        return { label: 'error', tone: 'bad' };
      case 'pending':
        return { label: 'waiting for policy', tone: 'none' };
      default:
        return { label: h.enforce_state, tone: 'none' };
    }
  }

  const detect = $derived(detectInfo(view));
  const enforce = $derived(enforceInfo(view));
  const needsInstall = $derived(
    isLinux && view !== null && (view.detect_state === 'no_permission' || (view.effective.enforce && view.enforce_state === 'no_permission'))
  );
  const sourceLine = $derived(view?.sources?.[0]?.message ?? '');

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
    const d = new Date(iso);
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  function toSeries(entries: SeriesEntry[]) {
    return entries.map((e) => ({ label: e.labels?.source ?? 'sshd', points: e.points }));
  }
</script>

<div class="space-y-6">
  {#if error}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      Failed to load security data: {error}
    </div>
  {/if}

  {#if !isLinux}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">IP banning</header>
      <div class="px-5 py-4 text-sm text-zinc-300 space-y-1">
        <p>Reactive IP banning runs on Linux agents only.</p>
        <p class="text-zinc-500 text-xs">The agent watches sshd authentication failures and blocks repeat offenders with nftables; neither is available on this host's operating system.</p>
      </div>
    </section>
  {:else}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <div class="grid grid-cols-1 sm:grid-cols-3 divide-y sm:divide-y-0 sm:divide-x divide-zinc-800">
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Detection</div>
          <div class="mt-1 text-lg font-semibold {toneText[detect.tone]}">{detect.label}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5 truncate" title={sourceLine}>{sourceLine || (view?.detect_message ?? '')}</div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Enforcement</div>
          <div class="mt-1 text-lg font-semibold {toneText[enforce.tone]}">{enforce.label}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5 truncate" title={view?.enforce_message ?? ''}>
            {#if view && !view.effective.enforce && view.detect_state === 'ok'}
              bans are recorded but not applied — turn enforcement on from the Security page
            {:else}
              {view?.enforce_message ?? ''}
            {/if}
          </div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">{view?.effective.enforce ? 'Active bans' : 'Would-be bans'}</div>
          <div class="mt-1 text-lg font-semibold text-zinc-100 numeric">{view?.active_local ?? active.length}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">
            {view?.fleet_applied ?? 0} fleet {(view?.fleet_applied ?? 0) === 1 ? 'entry' : 'entries'} applied
            {#if view?.reported_at}<span class="text-zinc-600">· reported {timeAgo(view.reported_at)}</span>{/if}
          </div>
        </div>
      </div>
      {#if needsInstall}
        <div class="px-4 sm:px-5 py-3 border-t border-amber-900/40 bg-amber-950/20 text-xs text-amber-100/90 space-y-2">
          <p>
            {#if view?.detect_state === 'no_permission'}
              The agent cannot read the system journal or auth log, so it sees no login attempts.
            {:else}
              The agent sees login attempts but cannot write bans to nftables.
            {/if}
            {' '}Reinstall in place with the IP-ban capability to grant journal access and <span class="font-mono">CAP_NET_ADMIN</span>:
          </p>
          <div class="flex items-center gap-2">
            <code class="min-w-0 flex-1 overflow-x-auto rounded-md bg-zinc-950/70 px-2.5 py-1.5 text-zinc-200 select-text whitespace-pre">{enableSnippet}</code>
            <button type="button" onclick={copySnippet} class="shrink-0 text-[11px] px-2 py-1 rounded bg-zinc-800 hover:bg-zinc-700 {copied === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
              {copied === 'done' ? 'copied' : copied === 'failed' ? 'copy failed' : 'copy'}
            </button>
          </div>
          {#if view?.detect_message || view?.enforce_message}
            <p class="text-amber-100/60 text-[11px] font-mono">{view?.detect_state === 'no_permission' ? view?.detect_message : view?.enforce_message}</p>
          {/if}
        </div>
      {/if}
    </section>

    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Auth failures per minute</header>
        <div class="px-3 py-3">
          <MultiChart series={toSeries(failures)} {fromMs} {toMs} zoomed={chartZoom !== null} loading={chartsLoading} height={180} yClampMin={0} yMinSpan={4} unit="/min" format={(v, e = 0) => `${v.toFixed(e)}/min`} onZoom={handleZoom} onResetZoom={handleReset} emptyText="No login failures in this range" />
        </div>
      </section>
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Bans per hour · active bans</header>
        <div class="px-3 py-3">
          <MultiChart series={[{ label: 'bans / hour', points: bans, bars: true }, { label: 'active', points: activeSeries }]} {fromMs} {toMs} zoomed={chartZoom !== null} loading={chartsLoading} height={180} stepped yClampMin={0} yMinSpan={4} format={(v, e = 0) => v.toFixed(e)} onZoom={handleZoom} onResetZoom={handleReset} emptyText="No bans in this range" />
        </div>
      </section>
    </div>

    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">{view?.effective.enforce ? 'Active bans on this host' : 'Would-be bans on this host'}</div>
        <div class="text-[11px] text-zinc-500 numeric">{active.length}</div>
      </header>
      {#if loading}
        <div class="p-4"><div class="h-24 rounded-lg shimmer"></div></div>
      {:else if active.length === 0}
        <div class="px-5 py-6 text-sm text-zinc-500">
          {#if view?.detect_state === 'ok'}
            No addresses are banned right now. Bans appear here after {view?.effective.enforce ? '' : 'they would have been applied — '}an address exceeds the failure threshold.
          {:else}
            Nothing to show until detection is running on this host.
          {/if}
        </div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
              <tr>
                <th class="text-left font-medium px-5 py-2.5">Address</th>
                <th class="text-left font-medium px-3 py-2.5 hidden sm:table-cell">User</th>
                <th class="text-right font-medium px-3 py-2.5 hidden sm:table-cell">Failures</th>
                <th class="text-left font-medium px-3 py-2.5">Banned</th>
                <th class="text-left font-medium px-3 py-2.5">Expires</th>
                <th class="text-left font-medium px-3 py-2.5">State</th>
                <th class="text-right font-medium px-5 py-2.5"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each active as b (b.ip)}
                <tr class="hover:bg-zinc-900/60">
                  <td class="px-5 py-2.5 font-mono text-zinc-200">
                    {b.ip}
                    {#if b.fleet}<span class="ml-2 rounded border border-sky-500/30 bg-sky-500/10 px-1 py-px text-[10px] uppercase tracking-wider text-sky-300">fleet</span>{/if}
                    {#if b.repeat_count > 1}<span class="ml-2 text-[10px] text-zinc-500">×{b.repeat_count}</span>{/if}
                  </td>
                  <td class="px-3 py-2.5 text-zinc-400 font-mono text-xs hidden sm:table-cell">{b.user || '—'}</td>
                  <td class="px-3 py-2.5 text-right numeric text-zinc-300 hidden sm:table-cell">{b.failures}</td>
                  <td class="px-3 py-2.5 text-zinc-400 numeric" title={absTime(b.banned_at)}>{timeAgo(b.banned_at)}</td>
                  <td class="px-3 py-2.5 text-zinc-400 numeric" title={absTime(b.expires_at)}>{timeUntil(b.expires_at)}</td>
                  <td class="px-3 py-2.5">
                    <span class="rounded-md border px-1.5 py-0.5 text-[10px] uppercase tracking-wider {b.enforced ? tonePill.bad : tonePill.info}">{b.enforced ? 'blocked' : 'observed'}</span>
                  </td>
                  <td class="px-5 py-2.5 text-right">
                    <button type="button" onclick={() => (toUnban = b)} class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Unban</button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    {#if stats && stats.total_bans > 0}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
        <header class="px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Top offenders · last 7 days</div>
          <p class="mt-0.5 text-[11px] text-zinc-500">
            {stats.total_bans} ban{stats.total_bans === 1 ? '' : 's'} against {stats.distinct_ips} address{stats.distinct_ips === 1 ? '' : 'es'}, independent of the range above
            {#if stats.repeat_ips > 0}<span class="text-amber-300"> · {stats.repeat_ips} came back for more</span>{/if}
          </p>
        </header>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
              <tr>
                <th class="text-left font-medium px-5 py-2.5">Address</th>
                <th class="text-right font-medium px-3 py-2.5">Bans</th>
                <th class="text-left font-medium px-3 py-2.5 hidden sm:table-cell">Last user</th>
                <th class="text-left font-medium px-3 py-2.5 hidden sm:table-cell">First seen</th>
                <th class="text-left font-medium px-5 py-2.5">Last seen</th>
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
                  <td class="px-3 py-2.5 font-mono text-xs text-zinc-400 hidden sm:table-cell">{o.user || '—'}</td>
                  <td class="px-3 py-2.5 text-zinc-400 numeric hidden sm:table-cell" title={absTime(o.first_seen)}>{timeAgo(o.first_seen)}</td>
                  <td class="px-5 py-2.5 text-zinc-400 numeric" title={absTime(o.last_seen)}>{timeAgo(o.last_seen)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/if}

    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <header class="flex flex-wrap items-center justify-between gap-2 px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Recent events</div>
        <a
          href={api.ipbanEventsCsvUrl({ host: hostId })}
          download
          class="inline-flex items-center gap-1.5 rounded-md border border-zinc-800 px-2.5 py-1 text-[11px] text-zinc-300 hover:text-zinc-100 hover:border-zinc-700"
          title="Download this host's full ban event history as CSV">
          <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
            <path d="M6 1.5v6m0 0L3.75 5.25M6 7.5l2.25-2.25M2 9.5h8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <span>CSV</span>
        </a>
      </header>
      {#if loading}
        <div class="p-4"><div class="h-16 rounded-lg shimmer"></div></div>
      {:else if events.length === 0}
        <div class="px-5 py-6 text-sm text-zinc-500">No ban activity recorded for this host yet.</div>
      {:else}
        <ul class="divide-y divide-zinc-800/70">
          {#each pagedEvents as ev (ev.id)}
            {@const a = actionLabel(ev.action, ev.enforced)}
            <li class="px-5 py-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              <span class="text-[11px] text-zinc-500 numeric w-24 shrink-0" title={absTime(ev.time)}>{timeAgo(ev.time)}</span>
              <span class="rounded-md border px-1.5 py-0.5 text-[10px] uppercase tracking-wider {tonePill[a.tone]}">{a.text}</span>
              <span class="font-mono text-zinc-200">{ev.ip}</span>
              <span class="text-xs text-zinc-500">
                {#if ev.action === 'ban'}
                  {ev.failures} failures{ev.user ? ` · last user ${ev.user}` : ''}{ev.expires_at ? ` · until ${absTime(ev.expires_at)}` : ''}
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
    </section>
  {/if}
</div>

{#snippet unbanBody()}
  {#if toUnban}
    <p class="text-sm text-zinc-300">
      Remove the ban on <span class="font-mono text-zinc-100">{toUnban.ip}</span> on this host. The agent applies it on its next check-in (within about a minute) and the address can be banned again if failures continue.
    </p>
    {#if toUnban.fleet}
      <p class="mt-2 text-xs text-amber-200/90">This address is also on the fleet blocklist; other hosts keep blocking it. Use the Security page to lift a fleet ban.</p>
    {/if}
  {/if}
{/snippet}

<ConfirmDialog
  open={toUnban !== null}
  title="Unban address"
  body={unbanBody}
  confirmLabel="Unban"
  onconfirm={doUnban}
  onclose={() => (toUnban = null)}
/>
