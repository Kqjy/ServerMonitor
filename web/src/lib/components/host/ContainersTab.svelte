<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import 'uplot';
  import { api, type ContainerRow } from '$lib/api';
  import { bytes, pct, timeAgo } from '$lib/format';
  import { TableSort, type SortColumn } from '$lib/sort.svelte';
  import ContainerDetail from './ContainerDetail.svelte';

  let { hostId, sampleIntervalS = 10 }: { hostId: number; sampleIntervalS?: number } = $props();

  type SortKey = 'state' | 'name' | 'cpu_pct' | 'mem_used' | 'mem_pct' | 'netio' | 'time';
  type Scope = 'all' | 'running' | 'notRunning';

  let rows = $state<ContainerRow[]>([]);
  let loaded = $state(false);
  let error = $state<string | null>(null);
  const sort = new TableSort<SortKey>('state');
  let atMs = $state<number | null>(null);
  let stepping = $state(false);
  let atOldest = $state(false);
  let expandedCid = $state<string | null>(null);
  let filter = $state('');
  let scope = $state<Scope>('all');
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
  const isUnsettled = (c: ContainerRow) => {
    const s = c.state.toLowerCase();
    return s === 'restarting' || s === 'paused' || s === 'removing' || s === 'dead';
  };
  const running = $derived(rows.filter(isRunning));
  const stopped = $derived(rows.filter((r) => !isRunning(r) && !isUnsettled(r)));
  const abnormal = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const c of rows) {
      if (!isUnsettled(c)) continue;
      const s = c.state.toLowerCase();
      counts.set(s, (counts.get(s) ?? 0) + 1);
    }
    return [...counts.entries()].map(([s, n]) => `${n} ${s}`);
  });

  const totals = $derived.by(() => {
    let cpu = 0;
    let mem = 0;
    for (const c of rows) {
      if (!isRunning(c)) continue;
      cpu += c.cpu_pct;
      mem += c.mem_used;
    }
    return { cpu, mem };
  });

  const memUnits = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  function memPair(used: number, limit: number): string {
    let i = 0;
    let v = limit;
    while (v >= 1024 && i < memUnits.length - 1) {
      v /= 1024;
      i++;
    }
    const scaled = used / 1024 ** i;
    if (scaled < 0.05) return `${bytes(used)} / ${bytes(limit)}`;
    return `${scaled.toFixed(1)} / ${v.toFixed(1)} ${memUnits[i]}`;
  }

  function memPct(c: ContainerRow): number | null {
    if (!c.mem_limit || c.mem_limit <= 0 || !isRunning(c)) return null;
    return (c.mem_used / c.mem_limit) * 100;
  }

  function stateTone(state: string): 'good' | 'warn' | 'bad' | 'idle' {
    const s = state.toLowerCase();
    if (s === 'running') return 'good';
    if (s === 'dead') return 'bad';
    if (s === 'restarting' || s === 'paused' || s === 'removing') return 'warn';
    return 'idle';
  }

  const stateDot: Record<string, string> = {
    good: 'bg-emerald-400',
    warn: 'bg-amber-400',
    bad: 'bg-rose-400',
    idle: 'bg-zinc-600'
  };
  const stateText: Record<string, string> = {
    good: 'text-emerald-300',
    warn: 'text-amber-300',
    bad: 'text-rose-300',
    idle: 'text-zinc-500'
  };

  function imageParts(img?: string): { repo: string; tag: string } {
    const s = img ?? '';
    const at = s.lastIndexOf('@');
    if (at > 0) return { repo: s.slice(0, at), tag: s.slice(at) };
    const colon = s.lastIndexOf(':');
    if (colon > s.lastIndexOf('/')) return { repo: s.slice(0, colon), tag: s.slice(colon) };
    return { repo: s, tag: '' };
  }

  const visible = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    return rows.filter((c) => {
      if (scope === 'running' && !isRunning(c)) return false;
      if (scope === 'notRunning' && isRunning(c)) return false;
      if (q === '') return true;
      return (
        c.name.toLowerCase().includes(q) ||
        c.cid.toLowerCase().includes(q) ||
        (c.image ?? '').toLowerCase().includes(q)
      );
    });
  });
  const isFiltered = $derived(filter.trim() !== '' || scope !== 'all');

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
      case 'mem_pct':
        return memPct(c) ?? -1;
      case 'netio':
        return (c.rx_bytes ?? 0) + (c.tx_bytes ?? 0);
      case 'time':
        return new Date(c.time).getTime();
    }
  }

  const sorted = $derived(sort.apply(visible, sortVal, (a, b) => b.cpu_pct - a.cpu_pct));

  const columns: SortColumn<SortKey>[] = [
    { key: 'name', label: 'Name', cls: 'text-left px-3 sm:px-5' },
    { label: 'Image', cls: 'text-left px-3 hidden md:table-cell' },
    { key: 'state', label: 'State', cls: 'text-left px-3 hidden sm:table-cell' },
    { key: 'cpu_pct', label: 'CPU', cls: 'text-right px-3' },
    { key: 'mem_used', label: 'Memory', cls: 'text-right px-3' },
    { key: 'mem_pct', label: 'Usage', cls: 'text-right px-3 hidden lg:table-cell' },
    { key: 'netio', label: 'Net I/O', cls: 'text-right px-3 hidden xl:table-cell' },
    { key: 'time', label: 'Updated', cls: 'text-right px-4 sm:px-5 hidden xl:table-cell' }
  ];
</script>

{#snippet memMeter(mp: number, compact: boolean)}
  <div class="flex items-center justify-end gap-1.5">
    <div class="h-1.5 {compact ? 'w-10' : 'w-14'} rounded-full bg-zinc-800 overflow-hidden">
      <div class="h-full {mp > 90 ? 'bg-rose-400' : mp > 75 ? 'bg-amber-400' : 'bg-emerald-400'}" style="width: {Math.min(100, mp).toFixed(1)}%"></div>
    </div>
    <span class="numeric text-right {compact ? 'w-7 text-[10px]' : 'w-9'} {mp > 90 ? 'text-rose-300' : mp > 75 ? 'text-amber-300' : 'text-zinc-300'}">{pct(mp, 0)}</span>
  </div>
{/snippet}

<div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
  <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-2">
    <div class="text-xs uppercase tracking-wider text-zinc-500">Containers</div>
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-zinc-500">
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
      {#if rows.length > 0}
        <span class="numeric text-zinc-400">
          {running.length} running{#if abnormal.length > 0}{' · '}<span class="text-amber-300">{abnormal.join(' · ')}</span>{/if} · {stopped.length} stopped
        </span>
        <span class="numeric text-zinc-500" title="Sum across running containers; CPU is core-relative, so 100% is one full core">
          {pct(totals.cpu, 0)} CPU · {bytes(totals.mem)}
        </span>
      {/if}
    </div>
  </header>

  {#if error}
    <div class="px-4 sm:px-5 py-3 border-b border-rose-900/40 bg-rose-950/30 text-sm text-rose-300">
      Failed to load containers: {error}
    </div>
  {/if}

  {#if loaded && rows.length > 0}
    <div class="px-4 sm:px-5 py-2 border-b border-zinc-800 flex flex-wrap items-center gap-2">
      <input
        type="text"
        placeholder="Filter name, image, or id…"
        bind:value={filter}
        class="flex-1 min-w-[12rem] bg-zinc-950 border border-zinc-800 rounded px-2 py-1 text-xs" />
      <div class="flex items-center gap-1 text-xs">
        {#each [{ v: 'all', l: 'All' }, { v: 'running', l: 'Running' }, { v: 'notRunning', l: 'Not running' }] as opt (opt.v)}
          <button
            type="button"
            onclick={() => (scope = opt.v as Scope)}
            class="px-2 py-1 rounded-md transition-colors {scope === opt.v ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
            {opt.l}
          </button>
        {/each}
      </div>
      {#if isFiltered}
        <span class="numeric text-xs text-zinc-500">{sorted.length} of {rows.length}</span>
      {/if}
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
  {:else if sorted.length === 0}
    <div class="px-4 sm:px-5 py-10 text-center text-sm text-zinc-500">
      No containers match this filter.
      <button
        type="button"
        onclick={() => { filter = ''; scope = 'all'; }}
        class="ml-2 text-zinc-300 underline underline-offset-2 hover:text-zinc-100">Clear</button>
    </div>
  {:else}
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
            {@const tone = stateTone(c.state)}
            {@const mp = memPct(c)}
            {@const img = imageParts(c.image)}
            <tr
              class="hover:bg-zinc-900/60 cursor-pointer {open ? 'bg-zinc-900/60' : ''}"
              onclick={() => onRowClick(c.cid)}>
              <td class="px-3 sm:px-5 py-2 text-zinc-100 font-mono text-xs">
                <button
                  type="button"
                  onclick={(e) => { e.stopPropagation(); toggleExpand(c.cid); }}
                  aria-expanded={open}
                  aria-controls={open ? `container-detail-${c.cid}` : undefined}
                  aria-label={`${open ? 'Hide' : 'Show'} details for container ${c.name}`}
                  class="flex min-w-0 max-w-full items-center gap-1.5 rounded px-1 -mx-1 py-0.5 -my-0.5 text-left">
                  <svg viewBox="0 0 24 24" aria-hidden="true" class="h-3 w-3 shrink-0 text-zinc-500 transition-transform {open ? 'rotate-90' : ''}" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6" /></svg>
                  <span aria-hidden="true" class="sm:hidden h-1.5 w-1.5 shrink-0 rounded-full {stateDot[tone]}" title={c.state}></span>
                  <span class="truncate max-w-[7rem] sm:max-w-[8rem] lg:max-w-[14rem]" title={c.name}>{c.name}</span>
                </button>
                <div class="text-[10px] leading-tight text-zinc-500 pl-[18px]">{c.cid}</div>
              </td>
              <td
                class="hidden md:table-cell px-3 py-2 font-mono text-xs cursor-text"
                onclick={(e) => e.stopPropagation()}>
                <div class="truncate max-w-[11rem] xl:max-w-[14rem]" title={c.image}>
                  <span class="text-zinc-400">{img.repo}</span><span class="text-zinc-600">{img.tag}</span>
                </div>
              </td>
              <td class="hidden sm:table-cell px-3 py-2 whitespace-nowrap">
                <span class="inline-flex items-center gap-1.5 text-xs">
                  <span class="h-1.5 w-1.5 rounded-full {stateDot[tone]}"></span>
                  <span class={stateText[tone]}>{c.state}</span>
                </span>
              </td>
              <td class="px-3 py-2 text-right numeric whitespace-nowrap {!isUp ? 'text-zinc-600' : c.cpu_pct > 70 ? 'text-rose-300' : c.cpu_pct > 30 ? 'text-amber-300' : 'text-zinc-300'}">{isUp ? pct(c.cpu_pct, 1) : '—'}</td>
              <td class="px-3 py-2 text-right numeric whitespace-nowrap text-zinc-300">
                {#if isUp}
                  {c.mem_limit ? memPair(c.mem_used, c.mem_limit) : bytes(c.mem_used)}
                  {#if mp !== null}
                    <div class="lg:hidden mt-1">{@render memMeter(mp, true)}</div>
                  {/if}
                {:else}
                  <span class="text-zinc-600">—</span>
                {/if}
              </td>
              <td class="hidden lg:table-cell px-3 py-2 text-right">
                {#if mp === null}
                  <span class="text-zinc-600">—</span>
                {:else}
                  {@render memMeter(mp, false)}
                {/if}
              </td>
              <td class="hidden xl:table-cell px-3 py-2 text-right numeric whitespace-nowrap text-zinc-300" title="Received / transmitted since the container started">
                {#if isUp}
                  <span class="text-zinc-600">rx</span> {bytes(c.rx_bytes ?? 0)}
                  <span class="text-zinc-600 ml-1.5">tx</span> {bytes(c.tx_bytes ?? 0)}
                {:else}
                  <span class="text-zinc-600">—</span>
                {/if}
              </td>
              <td class="hidden xl:table-cell px-4 sm:px-5 py-2 text-right text-zinc-500 text-xs numeric whitespace-nowrap">{timeAgo(c.time)}</td>
            </tr>
            {#if open}
              <tr class="bg-zinc-950/60">
                <td colspan={columns.length} class="p-0" id={`container-detail-${c.cid}`}>
                  <ContainerDetail {hostId} {sampleIntervalS} cid={c.cid} name={c.name} image={c.image} memLimit={c.mem_limit ?? null} at={atMs ?? lastDataMs} pinned={atMs !== null} />
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
