<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import 'uplot';
  import { api, type ProcessRow } from '$lib/api';
  import { bytes, pct, timeAgo } from '$lib/format';
  import { TableSort, type SortColumn } from '$lib/sort.svelte';
  import ProcessDetail from './ProcessDetail.svelte';

  let { hostId, sampleIntervalS = 10 }: { hostId: number; sampleIntervalS?: number } = $props();

  type SortKey = 'cpu_pct' | 'mem_rss' | 'pid' | 'name';

  let rows = $state<ProcessRow[]>([]);
  let loaded = $state(false);
  let error = $state<string | null>(null);
  let updatedAt = $state<string | null>(null);
  const sort = new TableSort<SortKey>('cpu_pct');
  let limit = $state(50);
  let atMs = $state<number | null>(null);
  let stepping = $state(false);
  let atOldest = $state(false);
  let expandedPid = $state<number | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;
  let refreshGen = 0;
  let inflight: AbortController | null = null;

  function toggleExpand(pid: number) {
    expandedPid = expandedPid === pid ? null : pid;
  }

  function onRowClick(pid: number) {
    const sel = typeof window !== 'undefined' ? window.getSelection()?.toString() ?? '' : '';
    if (sel.length > 0) return;
    toggleExpand(pid);
  }

  function maxRowTime(items: ProcessRow[]): string | null {
    let bestMs = 0;
    let best: string | null = null;
    for (const r of items) {
      const t = new Date(r.time).getTime();
      if (t > bestMs) {
        bestMs = t;
        best = r.time;
      }
    }
    return best;
  }

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    const opts: { limit: number; at?: string; signal: AbortSignal } = { limit, signal: ac.signal };
    if (atMs !== null) opts.at = new Date(atMs).toISOString();
    try {
      const fetched = await api.processes(hostId, opts);
      if (gen !== refreshGen) return;
      rows = fetched;
      updatedAt = maxRowTime(fetched);
      error = null;
      if (fetched.length > 0) atOldest = false;
    } catch (e) {
      if (gen !== refreshGen || (e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      if (gen === refreshGen) loaded = true;
    }
  }

  async function step(dir: 'prev' | 'next') {
    if (stepping) return;
    stepping = true;
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    try {
      const boundaryMs = atMs ?? (updatedAt ? new Date(updatedAt).getTime() : Date.now());
      const fetched = await api.processes(hostId, {
        limit,
        dir,
        at: new Date(boundaryMs).toISOString(),
        signal: ac.signal
      });
      if (gen !== refreshGen) return;
      error = null;
      if (fetched.length > 0) {
        rows = fetched;
        const newest = maxRowTime(fetched);
        updatedAt = newest;
        atMs = newest ? new Date(newest).getTime() : null;
        atOldest = false;
      } else if (dir === 'next') {
        atMs = null;
        atOldest = false;
        await refresh();
      } else {
        atOldest = true;
      }
    } catch (e) {
      if (gen !== refreshGen || (e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      stepping = false;
    }
  }

  let limitDirty = false;
  $effect(() => {
    void limit;
    if (limitDirty) untrack(refresh);
    limitDirty = true;
  });
  onMount(() => {
    refresh();
    timer = setInterval(() => {
      if (atMs !== null || stepping) return;
      refresh();
    }, 10_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    inflight?.abort();
  });

  function pad(n: number) {
    return String(n).padStart(2, '0');
  }
  function toLocalInputValue(ms: number): string {
    const d = new Date(ms);
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
  function onAtInput(e: Event) {
    const v = (e.currentTarget as HTMLInputElement).value;
    if (!v) {
      atMs = null;
      atOldest = false;
      refresh();
      return;
    }
    const d = new Date(v);
    if (isNaN(d.getTime())) return;
    atMs = d.getTime();
    atOldest = false;
    refresh();
  }
  function resetToNow() {
    atMs = null;
    atOldest = false;
    refresh();
  }

  const lastDataMs = $derived(updatedAt ? new Date(updatedAt).getTime() : null);
  const atLocal = $derived(
    atMs !== null
      ? toLocalInputValue(atMs)
      : lastDataMs !== null
        ? toLocalInputValue(lastDataMs)
        : ''
  );
  const isLive = $derived(atMs === null);
  const canStepPrev = $derived(!stepping && !atOldest);
  const canStepNext = $derived(!stepping && !isLive);

  const sorted = $derived(sort.apply(rows, (r) => r[sort.key]));

  const columns: SortColumn<SortKey>[] = [
    { key: 'pid', label: 'PID', cls: 'text-right px-3' },
    { key: 'name', label: 'Name', cls: 'text-left px-3' },
    { label: 'User', cls: 'text-left px-3' },
    { key: 'cpu_pct', label: 'CPU', cls: 'text-right px-3' },
    { key: 'mem_rss', label: 'RSS', cls: 'text-right px-3' },
    { label: 'Cmd', cls: 'text-left px-5' }
  ];
</script>

<div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
  <header class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-3 border-b border-zinc-800">
    <div class="text-xs uppercase tracking-wider text-zinc-500">Top processes</div>
    <div class="flex items-center gap-3 text-xs text-zinc-500">
      <div class="flex items-center gap-1">
        <button
          type="button"
          onclick={() => step('prev')}
          disabled={!canStepPrev}
          aria-label="Previous snapshot"
          title="Previous snapshot"
          class="p-1 rounded-md border border-zinc-800 text-zinc-300 hover:bg-zinc-800/40 disabled:opacity-40 disabled:cursor-default transition-colors">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 18 9 12 15 6" /></svg>
        </button>
        <input
          type="datetime-local"
          step="1"
          value={atLocal}
          oninput={onAtInput}
          aria-label="Snapshot timestamp"
          class="bg-zinc-950 border border-zinc-800 rounded px-2 py-1 text-xs numeric" />
        <button
          type="button"
          onclick={() => step('next')}
          disabled={!canStepNext}
          aria-label="Next snapshot"
          title="Next snapshot"
          class="p-1 rounded-md border border-zinc-800 text-zinc-300 hover:bg-zinc-800/40 disabled:opacity-40 disabled:cursor-default transition-colors">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6" /></svg>
        </button>
        <button
          type="button"
          onclick={resetToNow}
          disabled={isLive}
          class="ml-1 px-2 py-1 rounded-md border border-zinc-800 text-zinc-300 hover:bg-zinc-800/40 disabled:opacity-40 disabled:cursor-default transition-colors">
          Now
        </button>
      </div>
      <label class="flex items-center gap-2">
        Limit
        <select
          bind:value={limit}
          class="bg-zinc-950 border border-zinc-800 rounded px-2 py-1 text-xs">
          <option value={25}>25</option>
          <option value={50}>50</option>
          <option value={100}>100</option>
          <option value={250}>250</option>
        </select>
      </label>
      <span class="numeric inline-block text-right min-w-[7rem]">{isLive ? `updated ${timeAgo(updatedAt ?? undefined)}` : 'paused'}</span>
    </div>
  </header>

  {#if error}
    <div class="px-4 sm:px-5 py-3 border-b border-rose-900/40 bg-rose-950/30 text-sm text-rose-300">
      Failed to load processes: {error}
    </div>
  {/if}

  {#if !loaded}
    <div class="p-4">
      <div class="h-40 rounded-lg shimmer"></div>
    </div>
  {:else if rows.length === 0 && !error}
    <div class="p-12 text-center">
      <div class="mx-auto h-10 w-10 rounded-lg bg-zinc-800/70 grid place-items-center mb-4">
        <svg viewBox="0 0 24 24" class="h-5 w-5 text-zinc-400" fill="none" stroke="currentColor" stroke-width="1.6">
          <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
        </svg>
      </div>
      <h2 class="text-base font-medium text-zinc-100">No processes reported</h2>
      <p class="mt-1 text-sm text-zinc-500 max-w-md mx-auto">
        {isLive
          ? 'The agent has not reported any process snapshots for this host yet.'
          : 'No process data within 2 minutes of the selected moment.'}
      </p>
    </div>
  {:else if rows.length > 0}
  <div class="overflow-x-auto">
    <table class="w-full text-sm">
      <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
        <tr>
          {#each columns as col}
            <th class="{col.cls} font-medium py-2.5" aria-sort={sort.ariaSort(col.key)}>
              {#if col.key}
                {@const k = col.key}
                <button type="button" onclick={() => sort.toggle(k)} class="uppercase hover:text-zinc-300">{col.label}{sort.indicator(k)}</button>
              {:else}
                {col.label}
              {/if}
            </th>
          {/each}
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-800/70">
        {#each sorted as p (p.pid)}
          {@const open = expandedPid === p.pid}
          <tr
            class="hover:bg-zinc-900/60 cursor-pointer {open ? 'bg-zinc-900/60' : ''}"
            onclick={() => onRowClick(p.pid)}>
            <td class="px-3 py-1.5 text-right text-zinc-500 numeric">
              <button
                type="button"
                onclick={(e) => { e.stopPropagation(); toggleExpand(p.pid); }}
                aria-expanded={open}
                aria-controls={open ? `process-detail-${p.pid}` : undefined}
                aria-label={`${open ? 'Hide' : 'Show'} details for ${p.name}, pid ${p.pid}`}
                class="inline-flex items-center gap-1.5 rounded px-1 -mx-1 py-0.5 -my-0.5 hover:text-zinc-300 transition-colors">
                <svg viewBox="0 0 24 24" aria-hidden="true" class="h-3 w-3 transition-transform {open ? 'rotate-90' : ''}" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6" /></svg>
                {p.pid}
              </button>
            </td>
            <td class="px-3 py-1.5 text-zinc-200 font-mono text-xs whitespace-nowrap">{p.name}</td>
            <td class="px-3 py-1.5 text-zinc-500 text-xs whitespace-nowrap">{p.user || ''}</td>
            <td class="px-3 py-1.5 text-right numeric {p.cpu_pct > 70 ? 'text-rose-300' : p.cpu_pct > 30 ? 'text-amber-300' : 'text-zinc-300'}">{pct(p.cpu_pct, 1)}</td>
            <td class="px-3 py-1.5 text-right numeric text-zinc-300">{bytes(p.mem_rss)}</td>
            <td
              class="px-5 py-1.5 text-zinc-500 font-mono text-[11px] truncate max-w-md cursor-text"
              onclick={(e) => e.stopPropagation()}>{p.cmdline || ''}</td>
          </tr>
          {#if open}
            <tr class="bg-zinc-950/60">
              <td colspan="6" class="p-0" id={`process-detail-${p.pid}`}>
                <ProcessDetail {hostId} {sampleIntervalS} pid={p.pid} name={p.name} at={atMs ?? lastDataMs} live={isLive} />
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
  {/if}
</div>
