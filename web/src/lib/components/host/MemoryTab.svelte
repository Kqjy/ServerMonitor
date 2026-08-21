<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesPoint } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, isPreset, type Range } from '$lib/time';
  import { groupPoints, incrementalFromMs, mergePoints } from '$lib/series';
  import { bytes, pct } from '$lib/format';
  import MultiChart, { type ChartZoom } from '$lib/components/MultiChart.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range, sampleIntervalS = 10 }: { hostId: number; range: Range; sampleIntervalS?: number } = $props();

  let used = $state<SeriesPoint[]>([]);
  let cached = $state<SeriesPoint[]>([]);
  let buffers = $state<SeriesPoint[]>([]);
  let free = $state<SeriesPoint[]>([]);
  let swapUsed = $state<SeriesPoint[]>([]);
  let total = $state(0);
  let usedNow = $state(0);
  let availNow = $state(0);
  let swapTotal = $state(0);
  let swapUsedNow = $state(0);
  let fromMs = $state(0);
  let toMs = $state(0);
  let chartZoom = $state<ChartZoom>(null);
  let loadedStep = 0;
  let zoomFetched = false;
  let loading = $state(true);
  let masking = $state(false);
  let error = $state<string | null>(null);
  let refreshGen = 0;
  let inflight: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let prevRange: Range | null = null;

  const historySpecs = [
    { metric: 'mem_used' },
    { metric: 'mem_cached' },
    { metric: 'mem_buffers' },
    { metric: 'mem_free' },
    { metric: 'swap_used' }
  ];
  const liveSpecs = [
    { metric: 'mem_used' },
    { metric: 'mem_total' },
    { metric: 'mem_available' },
    { metric: 'swap_used' },
    { metric: 'swap_total' }
  ];
  let refreshBusy = false;

  async function refresh(incremental = false) {
    const zoomed = chartZoom;
    if (incremental && (refreshBusy || zoomed !== null || !isPreset(range) || loadedStep <= 0)) return;
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    refreshBusy = true;
    let from: string;
    let to: string | undefined;
    let replaceFromMs: number | undefined;
    if (incremental) {
      const now = Date.now();
      const b = rangeBoundsMs(range, now);
      replaceFromMs = Math.max(b.fromMs, incrementalFromMs(loadedStep, now));
      from = new Date(replaceFromMs).toISOString();
      fromMs = b.fromMs;
      toMs = b.toMs;
    } else if (zoomed) {
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
      const [history, live] = await Promise.all([
        api.seriesGroup({ host: hostId, series: historySpecs, from, to, step: incremental ? loadedStep : undefined, signal: ac.signal }),
        api.seriesGroup({ host: hostId, series: liveSpecs, from: '-2m', step: 10, signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      const nextUsed = groupPoints(history, 'mem_used');
      const nextCached = groupPoints(history, 'mem_cached');
      const nextBuffers = groupPoints(history, 'mem_buffers');
      const nextFree = groupPoints(history, 'mem_free');
      const nextSwapUsed = groupPoints(history, 'swap_used');
      if (incremental) {
        used = mergePoints(used, nextUsed, fromMs, toMs, replaceFromMs);
        cached = mergePoints(cached, nextCached, fromMs, toMs, replaceFromMs);
        buffers = mergePoints(buffers, nextBuffers, fromMs, toMs, replaceFromMs);
        free = mergePoints(free, nextFree, fromMs, toMs, replaceFromMs);
        swapUsed = mergePoints(swapUsed, nextSwapUsed, fromMs, toMs, replaceFromMs);
      } else {
        used = nextUsed;
        cached = nextCached;
        buffers = nextBuffers;
        free = nextFree;
        swapUsed = nextSwapUsed;
      }
      loadedStep = history.step_sec;
      zoomFetched = zoomed !== null;
      total = groupPoints(live, 'mem_total').at(-1)?.v ?? 0;
      usedNow = groupPoints(live, 'mem_used').at(-1)?.v ?? 0;
      availNow = groupPoints(live, 'mem_available').at(-1)?.v ?? 0;
      swapTotal = groupPoints(live, 'swap_total').at(-1)?.v ?? 0;
      swapUsedNow = groupPoints(live, 'swap_used').at(-1)?.v ?? 0;
      error = null;
      loading = false;
      masking = false;
    } catch (e) {
      if (gen !== refreshGen) return;
      if ((e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
      loading = false;
      masking = false;
    } finally {
      if (gen === refreshGen) {
        refreshBusy = false;
        if (inflight === ac) inflight = null;
      }
    }
  }

  $effect(() => {
    const current = range;
    if (prevRange !== null && !rangeEquals(current, prevRange)) {
      chartZoom = null;
      loading = true;
      used = [];
      cached = [];
      buffers = [];
      free = [];
      swapUsed = [];
    }
    prevRange = current;
  });

  $effect(() => {
    void range;
    untrack(() => refresh());
  });
  onMount(() => {
    timer = setInterval(() => { if (chartZoom === null) void refresh(true); }, 10_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    inflight?.abort();
  });

  const usedTone = $derived(total ? (usedNow / total > 0.9 ? 'bad' : usedNow / total > 0.75 ? 'warn' : 'good') : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const isZoomed = $derived(chartZoom !== null);
  function handleZoom(f: number, t: number) {
    chartZoom = { fromMs: f, toMs: t };
    fromMs = f;
    toMs = t;
    if (loadedStep > 0 && chooseStepSec(t - f, sampleIntervalS) < loadedStep) { masking = true; refresh(); }
  }
  function handleReset() {
    chartZoom = null;
    const b = rangeBoundsMs(range);
    fromMs = b.fromMs;
    toMs = b.toMs;
    if (zoomFetched) { masking = true; refresh(); }
  }
</script>

<div class="space-y-6">
  {#if error}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      Failed to load memory data: {error}
    </div>
  {/if}
  <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
    <StatCard label="Used" value={bytes(usedNow)} sub={total ? pct((usedNow / total) * 100, 1) : ''} tone={usedTone} {loading} />
    <StatCard label="Available" value={bytes(availNow)} {loading} />
    <StatCard label="Total" value={bytes(total)} {loading} />
    <StatCard label="Swap" value={swapTotal ? bytes(swapUsedNow) : 'none'} sub={swapTotal ? `${pct((swapUsedNow / swapTotal) * 100, 1)} of ${bytes(swapTotal)}` : ''} {loading} />
  </div>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Memory breakdown</div>
      <DownloadCsv host={hostId} metric="mem_used" {range} title="Download memory used (bytes) as CSV" />
    </header>
    <div class="px-3 py-3">
      <MultiChart
        series={[
          { label: 'Used', points: used },
          { label: 'Cached', points: cached },
          { label: 'Buffers', points: buffers },
          { label: 'Free', points: free }
        ]}
        {fromMs}
        {toMs}
        zoomed={isZoomed} {masking}
        {loading}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="B"
        format={(v, e = 0) => bytes(v, e)} />
    </div>
  </section>

  {#if swapTotal > 0}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Swap used</div>
        <DownloadCsv host={hostId} metric="swap_used" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart
          series={[{ label: 'Swap', color: 'oklch(0.83 0.18 85)', points: swapUsed }]}
          {fromMs}
          {toMs}
          zoomed={isZoomed} {masking}
          {loading}
          onZoom={handleZoom}
          onResetZoom={handleReset}
          unit="B"
          format={(v, e = 0) => bytes(v, e)}
          fill />
      </div>
    </section>
  {/if}
</div>
