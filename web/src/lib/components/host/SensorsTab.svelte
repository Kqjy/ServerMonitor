<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, type Range } from '$lib/time';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range }: { hostId: number; range: Range } = $props();

  let temps = $state<SeriesEntry[]>([]);
  let fromMs = $state(0);
  let toMs = $state(0);
  let chartZoom = $state<ChartZoom>(null);
  let loading = $state(true);
  let refreshGen = 0;
  let inflight: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let prevRange: Range | null = null;

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: e.labels.sensor ?? Object.values(e.labels).join(' '),
      points: e.points
    }));
  }

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
      const t = await api.seriesMulti({ host: hostId, metric: 'sensor_temp_c', from, to, splitBy: 'sensor', signal: ac.signal });
      if (gen !== refreshGen) return;
      temps = t.series;
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
      temps = [];
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

{#if loading}
  <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="px-5 py-3 border-b border-zinc-800"><div class="h-3 w-28 rounded shimmer"></div></header>
    <div class="px-3 py-3"><div class="h-[220px] rounded-md shimmer opacity-60"></div></div>
  </div>
{:else if temps.length === 0}
  <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-12 text-center">
    <h2 class="text-base font-medium text-zinc-100">No sensor data</h2>
    <p class="mt-1 text-sm text-zinc-500">The agent didn't expose temperature sensors on this host.</p>
  </div>
{:else}
  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Temperatures</div>
      <DownloadCsv host={hostId} metric="sensor_temp_c" splitBy="sensor" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart
        series={toSeries(temps)}
        {fromMs}
        {toMs}
        zoomed={isZoomed}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="°C"
        format={(v) => `${v.toFixed(0)} °C`} />
    </div>
  </section>
{/if}
