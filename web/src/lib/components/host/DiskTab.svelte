<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, type Range } from '$lib/time';
  import { bytes, pct } from '$lib/format';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';

  let { hostId, range }: { hostId: number; range: Range } = $props();

  let read = $state<SeriesEntry[]>([]);
  let write = $state<SeriesEntry[]>([]);
  let readOps = $state<SeriesEntry[]>([]);
  let writeOps = $state<SeriesEntry[]>([]);
  let fs = $state<{ mount: string; fstype: string; used: number; total: number; pct: number }[]>([]);
  let fromMs = $state(0);
  let toMs = $state(0);
  let chartZoom = $state<ChartZoom>(null);
  let loading = $state(true);
  let refreshGen = 0;
  let inflight: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let prevRange: Range | null = null;
  let frozenWindow: { fromMs: number; toMs: number } | null = null;

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: e.labels.device ?? Object.values(e.labels).join(' '),
      points: e.points
    }));
  }

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    let pageBounds: { fromMs: number; toMs: number };
    let from: string;
    let to: string | undefined;
    if (frozenWindow !== null) {
      pageBounds = frozenWindow;
      from = new Date(frozenWindow.fromMs).toISOString();
      to = new Date(frozenWindow.toMs).toISOString();
    } else {
      pageBounds = rangeBoundsMs(range);
      from = rangeToFrom(range);
      to = rangeToTo(range);
    }
    if (chartZoom === null) {
      fromMs = pageBounds.fromMs;
      toMs = pageBounds.toMs;
    }
    try {
      const [r, wr, rOps, wOps, fsTotal, fsUsed, fsPct] = await Promise.all([
        api.seriesMulti({ host: hostId, metric: 'disk_read_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_write_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_read_ops', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_write_ops', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_total', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used_pct', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      if (chartZoom === null) {
        fromMs = pageBounds.fromMs;
        toMs = pageBounds.toMs;
      }
      read = r.series;
      write = wr.series;
      readOps = rOps.series;
      writeOps = wOps.series;

      const byMount: Record<string, { mount: string; fstype: string; used: number; total: number; pct: number }> = {};
      for (const e of fsTotal.series) {
        const m = e.labels.mount;
        if (!m) continue;
        byMount[m] = { mount: m, fstype: e.labels.fstype ?? '', used: 0, total: e.points.at(-1)?.v ?? 0, pct: 0 };
      }
      for (const e of fsUsed.series) {
        const m = e.labels.mount;
        if (!m || !byMount[m]) continue;
        byMount[m].used = e.points.at(-1)?.v ?? 0;
      }
      for (const e of fsPct.series) {
        const m = e.labels.mount;
        if (!m || !byMount[m]) continue;
        byMount[m].pct = e.points.at(-1)?.v ?? 0;
      }
      fs = Object.values(byMount).sort((a, b) => b.pct - a.pct);
      loading = false;
    } catch (e) {
      if (gen !== refreshGen) return;
      if ((e as { name?: string })?.name === 'AbortError') return;
      loading = false;
    }
  }

  $effect(() => {
    const current = range;
    if (prevRange !== null && !rangeEquals(current, prevRange)) {
      chartZoom = null;
      frozenWindow = null;
      loading = true;
    }
    prevRange = current;
  });

  $effect(() => {
    void range;
    untrack(() => refresh());
  });
  onMount(() => {
    timer = setInterval(refresh, 10_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    inflight?.abort();
  });

  const isZoomed = $derived(chartZoom !== null);
  function handleZoom(f: number, t: number) {
    if (frozenWindow === null) {
      const b = rangeBoundsMs(range);
      frozenWindow = { fromMs: b.fromMs, toMs: b.toMs };
    }
    fromMs = f;
    toMs = t;
    chartZoom = { fromMs: f, toMs: t };
  }
  function handleReset() {
    chartZoom = null;
    frozenWindow = null;
  }
</script>

<div class="space-y-6">
  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Read throughput</header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(read)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v) => `${bytes(v, 0)}/s`} />
    </div>
  </section>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Write throughput</header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(write)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v) => `${bytes(v, 0)}/s`} />
    </div>
  </section>

  <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Read IOPS</header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(readOps)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="ops/s" format={(v) => `${v.toFixed(0)}/s`} />
      </div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Write IOPS</header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(writeOps)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="ops/s" format={(v) => `${v.toFixed(0)}/s`} />
      </div>
    </section>
  </div>

  {#if fs.length > 0}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Filesystems</header>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-5 py-2.5">Mount</th>
              <th class="text-left font-medium px-3 py-2.5">FS</th>
              <th class="text-right font-medium px-3 py-2.5">Used</th>
              <th class="text-right font-medium px-3 py-2.5">Total</th>
              <th class="text-right font-medium px-5 py-2.5">Usage</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each fs as row (row.mount)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-5 py-2 text-zinc-100 font-mono text-xs">{row.mount}</td>
                <td class="px-3 py-2 text-zinc-400 font-mono text-xs">{row.fstype}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-300">{bytes(row.used)}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-400">{bytes(row.total)}</td>
                <td class="px-5 py-2 text-right">
                  <div class="flex items-center justify-end gap-2">
                    <div class="h-1.5 w-24 rounded-full bg-zinc-800 overflow-hidden">
                      <div class="h-full {row.pct > 90 ? 'bg-rose-400' : row.pct > 75 ? 'bg-amber-400' : 'bg-emerald-400'}" style="width: {Math.min(100, row.pct).toFixed(1)}%"></div>
                    </div>
                    <span class="numeric text-zinc-300 w-12 text-right">{pct(row.pct, 0)}</span>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
</div>
