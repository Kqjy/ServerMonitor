<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type Host, type BatchSeriesResp } from '$lib/api';
  import { statusFor, timeAgo, pct, severityClass, bytes } from '$lib/format';
  import { subscribeHosts, subscribeAlerts } from '$lib/sse';
  import StatusDot from '$lib/components/StatusDot.svelte';

  type Usage = { cpu?: number; mem?: number; disk?: number; diskUsed?: number; diskTotal?: number; diskOverall?: boolean };

  let hosts = $state<Host[]>([]);
  let usage = $state<Record<number, Usage>>({});
  let loading = $state(true);
  let error = $state<string | null>(null);
  let storageNodeIds = $state<Set<number>>(new Set());
  let query = $state('');
  const visible = $derived(
    hosts
      .filter((h) => h.hostname.toLowerCase().includes(query.trim().toLowerCase()))
      .sort((a, b) => a.hostname.localeCompare(b.hostname, undefined, { sensitivity: 'base' }) || a.id - b.id)
  );
  const attentionCap = 6;
  let attentionExpanded = $state(false);

  const fleet = $derived.by(() => {
    let offline = 0;
    let firingHosts = 0;
    let firingTotal = 0;
    for (const h of hosts) {
      const s = statusFor(h.last_seen, h.sample_interval_s || 10);
      if (s === 'bad' || s === 'idle') offline++;
      const f = h.firing_alerts ?? 0;
      if (f > 0) {
        firingHosts++;
        firingTotal += f;
      }
    }
    return { total: hosts.length, offline, firingHosts, firingTotal };
  });

  const attention = $derived.by(() => {
    const out: { host: Host; rank: number; reasons: string[] }[] = [];
    for (const h of visible) {
      const s = statusFor(h.last_seen, h.sample_interval_s || 10);
      const firing = h.firing_alerts ?? 0;
      const backupState = storageNodeIds.has(h.id) ? '' : h.collector_status?.backup?.state ?? '';
      const reasons: string[] = [];
      let rank = 0;
      if (firing > 0) {
        const sev = h.firing_severity ?? 'info';
        rank = Math.max(rank, sev === 'critical' ? 100 : sev === 'warning' ? 70 : 20);
        reasons.push(`${firing} alert${firing === 1 ? '' : 's'} firing`);
      }
      if (s === 'bad') {
        rank = Math.max(rank, 90);
        reasons.push(`offline ${timeAgo(h.last_seen)}`);
      } else if (s === 'idle') {
        rank = Math.max(rank, 30);
        reasons.push('never reported');
      }
      if (backupState === 'agent_perms') {
        rank = Math.max(rank, 80);
        reasons.push('backups blocked');
      } else if (backupState === 'stale_agent') {
        rank = Math.max(rank, 50);
        reasons.push('stale backup agent');
      }
      if (h.upgrade_stalled) {
        rank = Math.max(rank, 40);
        reasons.push('agent update stalled');
      }
      if (rank > 0) out.push({ host: h, rank, reasons });
    }
    return out.sort((a, b) => b.rank - a.rank || a.host.hostname.localeCompare(b.host.hostname, undefined, { sensitivity: 'base' }));
  });

  const shownAttention = $derived(attentionExpanded ? attention : attention.slice(0, attentionCap));

  let timer: ReturnType<typeof setInterval> | null = null;
  let pointsUnsub: (() => void) | null = null;
  let alertUnsub: (() => void) | null = null;
  let pendingRefresh: ReturnType<typeof setTimeout> | null = null;

  async function loadUsage(targets: Host[]) {
    if (targets.length === 0) return;
    const ids = targets.map((h) => h.id);
    const [cpu, mem, disk, overallPct, overallUsed, overallTotal] = await Promise.all([
      api.seriesBatch({ hosts: ids, metric: 'cpu_total_pct', from: '-15m', step: 30 }).catch(() => null),
      api.seriesBatch({ hosts: ids, metric: 'mem_used_pct', from: '-15m', step: 30 }).catch(() => null),
      api.seriesBatch({ hosts: ids, metric: 'fs_used_pct', from: '-15m', step: 30, agg: 'max' }).catch(() => null),
      api.seriesBatch({ hosts: ids, metric: 'fs_overall_used_pct', from: '-15m', step: 30 }).catch(() => null),
      api.seriesBatch({ hosts: ids, metric: 'fs_overall_used', from: '-15m', step: 30 }).catch(() => null),
      api.seriesBatch({ hosts: ids, metric: 'fs_overall_total', from: '-15m', step: 30 }).catch(() => null)
    ]);
    if (!cpu && !mem && !disk && !overallPct && !overallUsed && !overallTotal) return;
    const next: Record<number, Usage> = { ...usage };
    for (const h of targets) next[h.id] = { ...(next[h.id] ?? {}) };
    const apply = (resp: BatchSeriesResp | null, key: 'cpu' | 'mem') => {
      if (!resp) return;
      for (const h of targets) {
        if (next[h.id][key] !== undefined) continue;
        const pts = resp.hosts[String(h.id)]?.points;
        const last = pts && pts[pts.length - 1];
        if (last) next[h.id][key] = last.v;
      }
    };
    apply(cpu, 'cpu');
    apply(mem, 'mem');
    for (const h of targets) {
      const overall = overallPct?.hosts[String(h.id)]?.points.at(-1);
      const legacy = disk?.hosts[String(h.id)]?.points.at(-1);
      const used = overallUsed?.hosts[String(h.id)]?.points.at(-1);
      const total = overallTotal?.hosts[String(h.id)]?.points.at(-1);
      if (overall) {
        next[h.id].disk = overall.v;
        next[h.id].diskOverall = true;
      } else if (legacy) {
        next[h.id].disk = legacy.v;
        next[h.id].diskOverall = false;
      }
      if (used) next[h.id].diskUsed = used.v;
      if (total) next[h.id].diskTotal = total.v;
    }
    usage = next;
  }

  async function refreshHosts() {
    try {
      const [list, nodeResp] = await Promise.all([api.hosts(), api.backupNodes().catch(() => null)]);
      hosts = list;
      if (nodeResp) storageNodeIds = new Set(nodeResp.nodes.map((n) => n.host_id));
      const fresh = list.filter((h) => usage[h.id] === undefined);
      if (fresh.length > 0) void loadUsage(fresh);
      if (!pointsUnsub) subscribe();
    } catch {}
  }

  async function bootstrap() {
    try {
      const [list, nodeResp] = await Promise.all([api.hosts(), api.backupNodes().catch(() => null)]);
      hosts = list;
      if (nodeResp) storageNodeIds = new Set(nodeResp.nodes.map((n) => n.host_id));
      error = null;
      await loadUsage(list);
      subscribe();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  function subscribe() {
    pointsUnsub?.();
    pointsUnsub = subscribeHosts(['cpu_total_pct', 'mem_used_pct', 'fs_used_pct', 'fs_overall_used_pct', 'fs_overall_used', 'fs_overall_total'], (hostId, points) => {
      const cur: Usage = { ...(usage[hostId] ?? {}) };
      let diskMax: number | undefined;
      for (const p of points) {
        if (p.metric === 'cpu_total_pct') cur.cpu = p.v;
        else if (p.metric === 'mem_used_pct') cur.mem = p.v;
        else if (p.metric === 'fs_overall_used_pct') {
          cur.disk = p.v;
          cur.diskOverall = true;
        } else if (p.metric === 'fs_overall_used') cur.diskUsed = p.v;
        else if (p.metric === 'fs_overall_total') cur.diskTotal = p.v;
        else if (p.metric === 'fs_used_pct' && !cur.diskOverall) diskMax = diskMax === undefined ? p.v : Math.max(diskMax, p.v);
      }
      if (diskMax !== undefined && !cur.diskOverall) cur.disk = diskMax;
      usage[hostId] = cur;
    });
  }

  function barColor(v: number): string {
    if (v >= 90) return 'bg-rose-500';
    if (v >= 70) return 'bg-amber-500';
    return 'bg-emerald-500';
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
    pointsUnsub?.();
    alertUnsub?.();
  });
</script>

{#snippet usageBar(label: string, value: number | undefined, title?: string)}
  <div class="flex items-center gap-2.5" {title}>
    <span class="w-9 shrink-0 text-[10px] uppercase tracking-wider text-zinc-500">{label}</span>
    <div class="flex-1 h-1.5 rounded-full bg-zinc-800 overflow-hidden">
      {#if value !== undefined}
        <div class="h-full rounded-full {barColor(value)} transition-[width] duration-500" style="width: {Math.min(100, Math.max(0, value))}%"></div>
      {/if}
    </div>
    <span class="w-9 shrink-0 text-right text-xs tabular-nums numeric {value !== undefined ? 'text-zinc-300' : 'text-zinc-600'}">{value !== undefined ? pct(value, 0) : '—'}</span>
  </div>
{/snippet}

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex flex-wrap items-end justify-between gap-3 mb-5 sm:mb-6">
    <div class="min-w-0">
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Hosts</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1 numeric">
        {#if loading || fleet.total === 0}
          All servers reporting to this instance
        {:else}
          <span class="text-zinc-300">{fleet.total}</span> host{fleet.total === 1 ? '' : 's'}
          {#if fleet.offline > 0}<span class="text-zinc-600"> · </span><span class="text-rose-300">{fleet.offline} offline</span>{/if}
          {#if fleet.firingTotal > 0}<span class="text-zinc-600"> · </span><span class="text-amber-300">{fleet.firingTotal} alert{fleet.firingTotal === 1 ? '' : 's'} firing</span>{/if}
          {#if fleet.offline === 0 && fleet.firingTotal === 0}<span class="text-zinc-600"> · </span><span class="text-emerald-300">all reporting normally</span>{/if}
        {/if}
      </p>
    </div>
    <div class="flex items-center gap-2 shrink-0">
      {#if hosts.length > 0}
        <input
          type="search"
          bind:value={query}
          placeholder="Filter hosts"
          aria-label="Filter hosts by name"
          class="w-36 sm:w-48 text-sm bg-zinc-900/60 border border-zinc-800 hover:border-zinc-700 focus:border-zinc-600 rounded-md px-3 py-1.5 text-zinc-200 placeholder:text-zinc-600 focus:outline-none transition-colors"
        />
      {/if}
      <a href="/hosts/new" class="text-sm text-zinc-400 hover:text-zinc-200 px-3 py-1.5 rounded-md border border-zinc-800 hover:border-zinc-700 transition-colors shrink-0">
        Add a host
      </a>
    </div>
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
  {:else if visible.length === 0}
    <div class="rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-8 text-center text-sm text-zinc-500">
      No hosts match <span class="text-zinc-300">{query}</span>.
    </div>
  {:else}
    {#if attention.length > 0 && attention.length < visible.length}
      <section class="mb-7">
        <div class="mb-2.5 flex flex-wrap items-center justify-between gap-2">
          <h2 class="text-xs uppercase tracking-wider text-zinc-400">
            Needs attention <span class="text-zinc-600 tabular-nums">({attention.length})</span>
          </h2>
          {#if attention.length > attentionCap}
            <button
              type="button"
              onclick={() => (attentionExpanded = !attentionExpanded)}
              aria-expanded={attentionExpanded}
              class="rounded-md px-2 py-1 text-[11px] uppercase tracking-wider text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/50 transition-colors">
              {attentionExpanded ? `Show top ${attentionCap}` : `Show all ${attention.length}`}
            </button>
          {/if}
        </div>
        <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {#each shownAttention as a (a.host.id)}
            {@render hostCard(a.host, a.reasons.join(' · '), a.rank >= 80 ? 'rose' : 'amber')}
          {/each}
        </div>
      </section>
      <h2 class="mb-2.5 text-xs uppercase tracking-wider text-zinc-400">
        All hosts <span class="text-zinc-600 tabular-nums">({visible.length})</span>
      </h2>
    {/if}
    {#if attention.length > 0 && attention.length === visible.length}
      <p class="mb-4 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-2.5 text-xs text-zinc-400">
        Every host below needs attention, so there is nothing to pin above.
      </p>
    {/if}
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
      {#each visible as h (h.id)}
        {@render hostCard(h)}
      {/each}
    </div>
  {/if}
</div>

{#snippet hostCard(h: Host, reason?: string, tone?: 'rose' | 'amber')}
  {@const s = statusFor(h.last_seen, h.sample_interval_s || 10)}
  {@const firing = h.firing_alerts ?? 0}
  {@const u = usage[h.id] ?? {}}
  {@const backupState = h.collector_status?.backup?.state}
  {@const backupMessage = h.collector_status?.backup?.message}
  {@const showBackupAgentBadge = !storageNodeIds.has(h.id)}
        <a href={`/hosts/${h.id}`}
           class="group rounded-lg border bg-zinc-900/40 hover:bg-zinc-900/70 p-4 transition-colors block {tone === 'rose' ? 'border-rose-900/60 hover:border-rose-800' : tone === 'amber' ? 'border-amber-900/60 hover:border-amber-800' : 'border-zinc-800 hover:border-zinc-700'}">
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
                {#if showBackupAgentBadge && backupState === 'stale_agent'}
                  <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/10 text-amber-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums" title={backupMessage}>
                    <span class="h-1.5 w-1.5 rounded-full bg-current"></span>
                    stale agent
                  </span>
                {:else if showBackupAgentBadge && backupState === 'agent_perms'}
                  <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-rose-500/40 bg-rose-500/10 text-rose-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums" title={backupMessage}>
                    <span class="h-1.5 w-1.5 rounded-full bg-current"></span>
                    backup blocked
                  </span>
                {/if}
                {#if h.update_available}
                  {#if h.externally_managed}
                    <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-zinc-700 bg-zinc-800/40 text-zinc-400 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums"
                          title="Managed externally — redeploy a new agent image to update">
                      managed
                    </span>
                  {:else if h.upgrade_stalled}
                    <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/10 text-amber-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums"
                          title={`agent self-update failing — still on ${h.agent_version ?? '?'}; redeploy or re-install`}>
                      <span class="h-1.5 w-1.5 rounded-full bg-current"></span>
                      stalled
                    </span>
                  {:else if h.upgrading}
                    <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-sky-500/40 bg-sky-500/10 text-sky-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums"
                          title={`updating agent ${h.agent_version ?? '?'} → ${h.latest_agent_version ?? '?'}`}>
                      <span class="h-1.5 w-1.5 rounded-full bg-current animate-pulse"></span>
                      updating
                    </span>
                  {:else}
                    <span class="shrink-0 inline-flex items-center gap-1 rounded-full border border-sky-500/40 bg-sky-500/10 text-sky-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider tabular-nums"
                          title={h.latest_agent_version ? `agent ${h.agent_version ?? '?'} → ${h.latest_agent_version}` : 'agent update available'}>
                      <span class="h-1.5 w-1.5 rounded-full bg-current"></span>
                      update
                    </span>
                  {/if}
                {/if}
              </div>
              <div class="mt-1 text-xs text-zinc-500 numeric">
                {h.os || '—'}{h.arch ? ` · ${h.arch}` : ''} · seen {timeAgo(h.last_seen)}
              </div>
              {#if reason}
                <div class="mt-1.5 text-xs numeric {tone === 'rose' ? 'text-rose-300' : 'text-amber-300'}">{reason}</div>
              {/if}
            </div>
          </div>
          <div class="mt-4 space-y-1.5">
            {@render usageBar('CPU', u.cpu)}
            {@render usageBar('MEM', u.mem)}
            {@render usageBar('DISK', u.disk, u.diskOverall && u.diskUsed !== undefined && u.diskTotal !== undefined ? `${bytes(u.diskUsed)} of ${bytes(u.diskTotal)} used across local filesystems` : u.disk !== undefined ? `fullest mount at ${u.disk.toFixed(0)}%` : undefined)}
          </div>
        </a>
{/snippet}
