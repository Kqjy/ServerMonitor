<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import 'uplot';
  import { api, type ContainerRow } from '$lib/api';
  import { bytes, pct, timeAgo } from '$lib/format';
  import { TableSort, type SortColumn } from '$lib/sort.svelte';
  import ContainerDetail from './ContainerDetail.svelte';

  let { hostId, sampleIntervalS = 10 }: { hostId: number; sampleIntervalS?: number } = $props();

  type SortKey = 'state' | 'name' | 'cpu_pct' | 'mem_used' | 'netio' | 'time';

  let rows = $state<ContainerRow[]>([]);
  let loaded = $state(false);
  let error = $state<string | null>(null);
  const sort = new TableSort<SortKey>('state');
  let atMs = $state<number | null>(null);
  let stepping = $state(false);
  let atOldest = $state(false);
  let expandedCid = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;
  let refreshGen = 0;
  let inflight: AbortController | null = null;

  function toggleExpand(cid: string) {
    expandedCid = expandedCid === cid ? null : cid;
  }

  function onRowClick(cid: string) {
    const sel = typeof window !== 'undefined' ? window.getSelection()?.toString() ?? '' : '';
    if (sel.length > 0) return;
    toggleExpand(cid);
  }

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    const opts: { at?: string; signal: AbortSignal } = { signal: ac.signal };
    if (atMs !== null) opts.at = new Date(atMs).toISOString();
    try {
      const fetched = await api.containers(hostId, opts);
      if (gen !== refreshGen) return;
      rows = fetched;
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
      const boundaryMs = atMs ?? lastDataMs ?? Date.now();
      const fetched = await api.containers(hostId, {
        dir,
        at: new Date(boundaryMs).toISOString(),
        signal: ac.signal
      });
      if (gen !== refreshGen) return;
      error = null;
      if (fetched.length > 0) {
        rows = fetched;
        let max = 0;
        for (const r of fetched) {
          const t = new Date(r.time).getTime();
          if (t > max) max = t;
        }
        atMs = max > 0 ? max : null;
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

  const lastDataMs = $derived.by(() => {
    let max = 0;
    for (const r of rows) {
      const t = new Date(r.time).getTime();
      if (t > max) max = t;
    }
    return max > 0 ? max : null;
  });
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
  const isRunning = (c: ContainerRow) => c.state.toLowerCase() === 'running';
  const running = $derived(rows.filter(isRunning));
  const stopped = $derived(rows.filter((r) => !isRunning(r)));

  function sortVal(c: ContainerRow): number | string {
    switch (sort.key) {
      case 'state':
        return c.state.toLowerCase();
      case 'name':
        return c.name;
      case 'cpu_pct':
        return c.cpu_pct;
      case 'mem_used':
        return c.mem_used;
      case 'netio':
        return (c.rx_bytes ?? 0) + (c.tx_bytes ?? 0);
      case 'time':
        return new Date(c.time).getTime();
    }
  }

  const sorted = $derived(sort.apply(rows, sortVal, (a, b) => b.cpu_pct - a.cpu_pct));

  const columns: SortColumn<SortKey>[] = [
    { key: 'name', label: 'Name', cls: 'text-left px-5' },
    { label: 'Image', cls: 'text-left px-3' },
    { key: 'state', label: 'State', cls: 'text-left px-3' },
    { key: 'cpu_pct', label: 'CPU', cls: 'text-right px-3' },
    { key: 'mem_used', label: 'Memory', cls: 'text-right px-3' },
    { key: 'netio', label: 'Net I/O', cls: 'text-right px-3' },
    { key: 'time', label: 'Updated', cls: 'text-right px-5' }
  ];
</script>

<div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
  <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-2">
    <div class="text-xs uppercase tracking-wider text-zinc-500">Containers</div>
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
      <span class="numeric text-zinc-400">{running.length} running · {stopped.length} stopped</span>
    </div>
  </header>

  {#if error}
    <div class="px-4 sm:px-5 py-3 border-b border-rose-900/40 bg-rose-950/30 text-sm text-rose-300">
      Failed to load containers: {error}
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
          <path d="M3 8h18l-2 11H5L3 8Z" /><path d="M8 8V5h8v3" />
        </svg>
      </div>
      <h2 class="text-base font-medium text-zinc-100">No containers detected</h2>
      <p class="mt-1 text-sm text-zinc-500 max-w-md mx-auto">
        {isLive
          ? "The agent didn't find a reachable Docker daemon. If one is running, ensure the agent has access to the docker socket."
          : 'No container data within 5 minutes of the selected moment.'}
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
          {#each sorted as c (c.cid)}
            {@const isUp = isRunning(c)}
            {@const open = expandedCid === c.cid}
            <tr
              class="hover:bg-zinc-900/60 cursor-pointer {open ? 'bg-zinc-900/60' : ''}"
              onclick={() => onRowClick(c.cid)}>
              <td class="px-5 py-2 text-zinc-100 font-mono text-xs whitespace-nowrap">
                <div class="inline-flex items-center gap-1.5">
                  <svg viewBox="0 0 24 24" class="h-3 w-3 text-zinc-500 transition-transform {open ? 'rotate-90' : ''}" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6" /></svg>
                  <span>{c.name}</span>
                </div>
                <div class="text-[10px] text-zinc-500 pl-[18px]">{c.cid}</div>
              </td>
              <td
                class="px-3 py-2 text-zinc-400 font-mono text-xs cursor-text"
                onclick={(e) => e.stopPropagation()}>{c.image}</td>
              <td class="px-3 py-2">
                <span class="inline-flex items-center gap-1.5 text-xs">
                  <span class={`h-1.5 w-1.5 rounded-full ${isUp ? 'bg-emerald-400' : 'bg-zinc-600'}`}></span>
                  <span class={isUp ? 'text-emerald-300' : 'text-zinc-500'}>{c.state}</span>
                </span>
              </td>
              <td class="px-3 py-2 text-right numeric text-zinc-300">{isUp ? pct(c.cpu_pct, 1) : '—'}</td>
              <td class="px-3 py-2 text-right numeric text-zinc-300">{isUp ? `${bytes(c.mem_used)}${c.mem_limit ? ` / ${bytes(c.mem_limit)}` : ''}` : '—'}</td>
              <td class="px-3 py-2 text-right numeric text-zinc-300">{isUp ? `${bytes(c.rx_bytes ?? 0)} / ${bytes(c.tx_bytes ?? 0)}` : '—'}</td>
              <td class="px-5 py-2 text-right text-zinc-500 text-xs numeric">{timeAgo(c.time)}</td>
            </tr>
            {#if open}
              <tr class="bg-zinc-950/60">
                <td colspan="7" class="p-0">
                  <ContainerDetail {hostId} {sampleIntervalS} cid={c.cid} at={atMs ?? lastDataMs} pinned={atMs !== null} />
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
