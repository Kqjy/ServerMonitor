<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesPoint } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, type Range } from '$lib/time';
  import { bytes, pct } from '$lib/format';
  import MultiChart, { type ChartZoom } from '$lib/components/MultiChart.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range }: { hostId: number; range: Range } = $props();

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
  let loading = $state(true);
  let refreshGen = 0;
  let inflight: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let prevRange: Range | null = null;

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    let from: string;
    let to: string | undefined;
    const b = rangeBoundsMs(range);
    from = rangeToFrom(range);
    to = rangeToTo(range);
    if (chartZoom === null) {
      fromMs = b.fromMs;
      toMs = b.toMs;
    }
    try {
      const [u, c, b2, f, su, t, av, st] = await Promise.all([
        api.series({ host: hostId, metric: 'mem_used', from, to, signal: ac.signal }),
        api.series({ host: hostId, metric: 'mem_cached', from, to, signal: ac.signal }),
        api.series({ host: hostId, metric: 'mem_buffers', from, to, signal: ac.signal }),
        api.series({ host: hostId, metric: 'mem_free', from, to, signal: ac.signal }),
        api.series({ host: hostId, metric: 'swap_used', from, to, signal: ac.signal }),
        api.series({ host: hostId, metric: 'mem_total', from: '-2m', step: 10, signal: ac.signal }),
        api.series({ host: hostId, metric: 'mem_available', from: '-2m', step: 10, signal: ac.signal }),
        api.series({ host: hostId, metric: 'swap_total', from: '-2m', step: 10, signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      used = u.points;
      cached = c.points;
      buffers = b2.points;
      free = f.points;
      swapUsed = su.points;
      total = t.points.at(-1)?.v ?? 0;
      usedNow = u.points.at(-1)?.v ?? 0;
      availNow = av.points.at(-1)?.v ?? 0;
      swapTotal = st.points.at(-1)?.v ?? 0;
      swapUsedNow = su.points.at(-1)?.v ?? 0;
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
    timer = setInterval(refresh, 10_000);
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
  }
  function handleReset() {
    chartZoom = null;
    const b = rangeBoundsMs(range);
    fromMs = b.fromMs;
    toMs = b.toMs;
  }
</script>

<div class="space-y-6">
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
        zoomed={isZoomed}
        {loading}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="B"
        format={(v) => bytes(v, 0)} />
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
          zoomed={isZoomed}
          {loading}
          onZoom={handleZoom}
          onResetZoom={handleReset}
          unit="B"
          format={(v) => bytes(v, 0)}
          fill />
      </div>
    </section>
  {/if}
</div>
