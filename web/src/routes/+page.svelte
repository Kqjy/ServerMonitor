<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type Host, type SeriesPoint } from '$lib/api';
  import { statusFor, timeAgo, pct, severityClass } from '$lib/format';
  import { subscribeHost, subscribeAlerts, appendLive, rangeWindowMs } from '$lib/sse';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  let hosts = $state<Host[]>([]);
  let sparks = $state<Record<number, SeriesPoint[]>>({});
  let lastValues = $state<Record<number, number>>({});
  let loading = $state(true);
  let error = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;
  let unsubs: Array<() => void> = [];
  let alertUnsub: (() => void) | null = null;
  let pendingRefresh: ReturnType<typeof setTimeout> | null = null;

  async function refreshHosts() {
    try {
      const list = await api.hosts();
      const changed = list.length !== hosts.length;
      hosts = list;
      if (changed) resubscribe();
    } catch {}
  }

  async function bootstrap() {
    try {
      const list = await api.hosts();
      hosts = list;
      error = null;
      await Promise.all(
        list.map(async (h) => {
          try {
            const r = await api.series({
              host: h.id,
              metric: 'cpu_total_pct',
              from: '-15m',
              step: 30
            });
            sparks[h.id] = r.points;
            const last = r.points[r.points.length - 1];
            if (last) lastValues[h.id] = last.v;
          } catch {}
        })
      );
      resubscribe();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  function resubscribe() {
    unsubs.forEach((u) => u());
    unsubs = hosts.map((h) =>
      subscribeHost(h.id, ['cpu_total_pct'], (points) => {
        const win = rangeWindowMs('15m');
        for (const p of points) {
          if (p.metric !== 'cpu_total_pct') continue;
          sparks[h.id] = appendLive(sparks[h.id] ?? [], { ts: p.ts, v: p.v }, win);
          lastValues[h.id] = p.v;
        }
      })
    );
  }

  onMount(() => {
    bootstrap();
    timer = setInterval(refreshHosts, 30_000);
    alertUnsub = subscribeAlerts(() => {
      if (pendingRefresh) return;
      pendingRefresh = setTimeout(() => {
        pendingRefresh = null;
        void refreshHosts();
      }, 500);
    });
  });

  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (pendingRefresh) clearTimeout(pendingRefresh);
    unsubs.forEach((u) => u());
    alertUnsub?.();
  });
</script>

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex flex-wrap items-end justify-between gap-3 mb-5 sm:mb-6">
    <div class="min-w-0">
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Hosts</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1">All servers reporting to this instance</p>
    </div>
    <a href="/hosts/new" class="text-sm text-zinc-400 hover:text-zinc-200 px-3 py-1.5 rounded-md border border-zinc-800 hover:border-zinc-700 transition-colors shrink-0">
      Add a host
    </a>
  </div>

  {#if error}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      {error}
    </div>
  {:else if loading}
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
      {#each Array(3) as _, i (i)}
        <div class="h-40 rounded-lg border border-zinc-800/50 shimmer"></div>
      {/each}
    </div>
  {:else if hosts.length === 0}
    <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-12 text-center">
      <div class="mx-auto h-10 w-10 rounded-lg bg-zinc-800/70 grid place-items-center mb-4">
        <svg viewBox="0 0 24 24" class="h-5 w-5 text-zinc-400" fill="none" stroke="currentColor" stroke-width="1.6">
          <rect x="3" y="4" width="18" height="6" rx="1.5" />
          <rect x="3" y="14" width="18" height="6" rx="1.5" />
          <circle cx="7" cy="7" r="0.6" fill="currentColor" />
          <circle cx="7" cy="17" r="0.6" fill="currentColor" />
        </svg>
      </div>
      <h2 class="text-base font-medium text-zinc-100">No hosts registered yet</h2>
      <p class="mt-1 text-sm text-zinc-500 max-w-md mx-auto">
        Add a host — you'll get a one-line install command for Linux, Windows, or macOS.
      </p>
      <a href="/hosts/new" class="mt-5 inline-block text-sm text-emerald-200 px-3.5 py-2 rounded-md bg-emerald-500/20 hover:bg-emerald-500/30 border border-emerald-500/40">
        Add a host →
      </a>
    </div>
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
      {#each hosts as h (h.id)}
        {@const s = statusFor(h.last_seen, h.sample_interval_s || 10)}
        {@const firing = h.firing_alerts ?? 0}
        <a href={`/hosts/${h.id}`}
           class="group rounded-lg border border-zinc-800 hover:border-zinc-700 bg-zinc-900/40 hover:bg-zinc-900/70 p-4 transition-colors block">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2 flex-wrap">
                <StatusDot status={s} />
                <span class="font-medium text-zinc-100 truncate">{h.hostname}</span>
                {#if firing > 0}
                  <span class="shrink-0 inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums {severityClass(h.firing_severity ?? '', 'chip')}">
                    <span class="h-1.5 w-1.5 rounded-full bg-current"></span>
                    {firing} {firing === 1 ? 'alert' : 'alerts'}
                  </span>
                {/if}
                {#if h.update_available}
                  <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-sky-500/40 bg-sky-500/10 text-sky-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums"
                        title={h.latest_agent_version ? `agent ${h.agent_version ?? '?'} → ${h.latest_agent_version}` : 'agent update available'}>
                    <span class="h-1.5 w-1.5 rounded-full bg-current"></span>
                    update
                  </span>
                {/if}
              </div>
              <div class="mt-1 text-xs text-zinc-500 numeric">
                {h.os || '—'}{h.arch ? ` · ${h.arch}` : ''} · seen {timeAgo(h.last_seen)}
              </div>
            </div>
            <div class="text-right">
              <div class="text-2xl font-semibold numeric tabular-nums text-zinc-100">
                {lastValues[h.id] !== undefined ? pct(lastValues[h.id], 2) : '—'}
              </div>
              <div class="text-[10px] uppercase tracking-wider text-zinc-500">cpu</div>
            </div>
          </div>
          <div class="mt-4">
            <Sparkline points={sparks[h.id] ?? []} />
          </div>
        </a>
      {/each}
    </div>
  {/if}
</div>
