<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, isPreset, type Range } from '$lib/time';
  import { groupSeries, incrementalFromMs, mergeSeries } from '$lib/series';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range, sampleIntervalS = 10 }: { hostId: number; range: Range; sampleIntervalS?: number } = $props();

  let temps = $state<SeriesEntry[]>([]);
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
  const historySpecs = [{ metric: 'sensor_temp_c', splitBy: 'sensor' }];
  let refreshBusy = false;

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: e.labels.sensor ?? Object.values(e.labels).join(' '),
      points: e.points
    }));
  }

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
      const history = await api.seriesGroup({
        host: hostId,
        series: historySpecs,
        from,
        to,
        step: incremental ? loadedStep : undefined,
        signal: ac.signal
      });
      if (gen !== refreshGen) return;
      const nextTemps = groupSeries(history, 'sensor_temp_c');
      temps = incremental
        ? mergeSeries(temps, nextTemps, fromMs, toMs, 'sensor', replaceFromMs)
        : nextTemps;
      loadedStep = history.step_sec;
      zoomFetched = zoomed !== null;
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
      temps = [];
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

{#if error && !loading}
  <div class="mb-4 rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
    Failed to load sensor data: {error}
  </div>
{/if}
{#if loading}
  <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="px-5 py-3 border-b border-zinc-800"><div class="h-3 w-28 rounded shimmer"></div></header>
    <div class="px-3 py-3"><div class="h-[220px] rounded-md shimmer opacity-60"></div></div>
  </div>
{:else if temps.length === 0 && !error}
  <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-12 text-center">
    <h2 class="text-base font-medium text-zinc-100">No sensor data</h2>
    <p class="mt-1 text-sm text-zinc-500">The agent didn't expose temperature sensors on this host.</p>
  </div>
{:else if temps.length > 0}
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
        zoomed={isZoomed} {masking}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="°C"
        format={(v, e = 0) => `${v.toFixed(e)} °C`} />
    </div>
  </section>
{/if}
