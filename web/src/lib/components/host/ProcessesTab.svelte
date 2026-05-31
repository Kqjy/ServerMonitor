<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import 'uplot';
  import { api, type ProcessRow } from '$lib/api';
  import { bytes, pct, timeAgo } from '$lib/format';
  import ProcessDetail from './ProcessDetail.svelte';

  let { hostId, sampleIntervalS = 10 }: { hostId: number; sampleIntervalS?: number } = $props();

  let rows = $state<ProcessRow[]>([]);
  let updatedAt = $state<string | null>(null);
  let sortKey = $state<'cpu_pct' | 'mem_rss' | 'pid' | 'name'>('cpu_pct');
  let sortDir = $state<'asc' | 'desc'>('desc');
  let limit = $state(50);
  let atMs = $state<number | null>(null);
  let stepping = $state(false);
  let expandedPid = $state<number | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

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
    const opts: { limit: number; at?: string } = { limit };
    if (atMs !== null) opts.at = new Date(atMs).toISOString();
    const fetched = await api.processes(hostId, opts);
    if (fetched.length > 0) {
      rows = fetched;
      updatedAt = maxRowTime(fetched);
    } else if (atMs === null && rows.length === 0) {
      const fallback = await api.processes(hostId, { limit, dir: 'prev' });
      rows = fallback;
      updatedAt = maxRowTime(fallback);
    } else if (atMs !== null) {
      rows = [];
      updatedAt = null;
    }
  }

  async function step(dir: 'prev' | 'next') {
    if (stepping) return;
    stepping = true;
    try {
      const boundaryMs = atMs ?? (updatedAt ? new Date(updatedAt).getTime() : Date.now());
      const fetched = await api.processes(hostId, {
        limit,
        dir,
        at: new Date(boundaryMs).toISOString()
      });
      if (fetched.length > 0) {
        rows = fetched;
        const newest = maxRowTime(fetched);
        updatedAt = newest;
        atMs = newest ? new Date(newest).getTime() : null;
      } else if (dir === 'next') {
        atMs = null;
        await refresh();
      } else {
        rows = [];
        updatedAt = null;
      }
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
      if (atMs !== null) return;
      refresh();
    }, 10_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
  });

  function toggleSort(k: typeof sortKey) {
    if (sortKey === k) sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    else {
      sortKey = k;
      sortDir = 'desc';
    }
  }

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
      refresh();
      return;
    }
    const d = new Date(v);
    if (isNaN(d.getTime())) return;
    atMs = d.getTime();
    refresh();
  }
  function resetToNow() {
    atMs = null;
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

  const sorted = $derived.by(() => {
    const cp = [...rows];
    cp.sort((a, b) => {
      const av = a[sortKey] as number | string;
      const bv = b[sortKey] as number | string;
      if (typeof av === 'number' && typeof bv === 'number') {
        return sortDir === 'asc' ? av - bv : bv - av;
      }
      return sortDir === 'asc' ? String(av).localeCompare(String(bv)) : String(bv).localeCompare(String(av));
    });
    return cp;
  });
</script>

<div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
  <header class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-3 border-b border-zinc-800">
    <div class="text-xs uppercase tracking-wider text-zinc-500">Top processes</div>
    <div class="flex items-center gap-3 text-xs text-zinc-500">
      <div class="flex items-center gap-1">
        <button
          type="button"
          onclick={() => step('prev')}
          disabled={stepping}
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
          disabled={isLive || stepping}
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

  <div class="overflow-x-auto">
    <table class="w-full text-sm">
      <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
        <tr>
          <th class="text-right font-medium px-3 py-2.5">
            <button type="button" onclick={() => toggleSort('pid')} class="hover:text-zinc-300">PID{sortKey === 'pid' ? (sortDir === 'asc' ? ' ↑' : ' ↓') : ''}</button>
          </th>
          <th class="text-left font-medium px-3 py-2.5">
            <button type="button" onclick={() => toggleSort('name')} class="hover:text-zinc-300">Name{sortKey === 'name' ? (sortDir === 'asc' ? ' ↑' : ' ↓') : ''}</button>
          </th>
          <th class="text-left font-medium px-3 py-2.5">User</th>
          <th class="text-right font-medium px-3 py-2.5">
            <button type="button" onclick={() => toggleSort('cpu_pct')} class="hover:text-zinc-300">CPU{sortKey === 'cpu_pct' ? (sortDir === 'asc' ? ' ↑' : ' ↓') : ''}</button>
          </th>
          <th class="text-right font-medium px-3 py-2.5">
            <button type="button" onclick={() => toggleSort('mem_rss')} class="hover:text-zinc-300">RSS{sortKey === 'mem_rss' ? (sortDir === 'asc' ? ' ↑' : ' ↓') : ''}</button>
          </th>
          <th class="text-left font-medium px-5 py-2.5">Cmd</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-800/70">
        {#each sorted as p (p.pid)}
          {@const open = expandedPid === p.pid}
          <tr
            class="hover:bg-zinc-900/60 cursor-pointer {open ? 'bg-zinc-900/60' : ''}"
            onclick={() => onRowClick(p.pid)}>
            <td class="px-3 py-1.5 text-right text-zinc-500 numeric">
              <span class="inline-flex items-center gap-1.5">
                <svg viewBox="0 0 24 24" class="h-3 w-3 transition-transform {open ? 'rotate-90' : ''}" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6" /></svg>
                {p.pid}
              </span>
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
              <td colspan="6" class="p-0">
                <ProcessDetail {hostId} {sampleIntervalS} pid={p.pid} name={p.name} at={atMs ?? lastDataMs} live={isLive} />
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
</div>
