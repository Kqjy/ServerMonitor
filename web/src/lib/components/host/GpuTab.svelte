<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, isPreset, type Range } from '$lib/time';
  import { groupSeries, incrementalFromMs, mergeSeries } from '$lib/series';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range, sampleIntervalS = 10 }: { hostId: number; range: Range; sampleIntervalS?: number } = $props();

  let usage = $state<SeriesEntry[]>([]);
  let memUsedPct = $state<SeriesEntry[]>([]);
  let temp = $state<SeriesEntry[]>([]);
  let power = $state<SeriesEntry[]>([]);
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
    { metric: 'gpu_usage_pct', splitBy: 'gpu' },
    { metric: 'gpu_mem_used_pct', splitBy: 'gpu' },
    { metric: 'gpu_temp_c', splitBy: 'gpu' },
    { metric: 'gpu_power_w', splitBy: 'gpu' }
  ];
  let refreshBusy = false;

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: `GPU ${e.labels.gpu}${e.labels.name ? ' · ' + e.labels.name : ''}`,
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
      const nextUsage = groupSeries(history, 'gpu_usage_pct');
      const nextMem = groupSeries(history, 'gpu_mem_used_pct');
      const nextTemp = groupSeries(history, 'gpu_temp_c');
      const nextPower = groupSeries(history, 'gpu_power_w');
      if (incremental) {
        usage = mergeSeries(usage, nextUsage, fromMs, toMs, 'gpu', replaceFromMs);
        memUsedPct = mergeSeries(memUsedPct, nextMem, fromMs, toMs, 'gpu', replaceFromMs);
        temp = mergeSeries(temp, nextTemp, fromMs, toMs, 'gpu', replaceFromMs);
        power = mergeSeries(power, nextPower, fromMs, toMs, 'gpu', replaceFromMs);
      } else {
        usage = nextUsage;
        memUsedPct = nextMem;
        temp = nextTemp;
        power = nextPower;
      }
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
      usage = [];
      memUsedPct = [];
      temp = [];
      power = [];
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

  const hasData = $derived(usage.length + memUsedPct.length + temp.length + power.length > 0);
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
    Failed to load GPU data: {error}
  </div>
{/if}
{#if loading}
  <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
    {#each Array(4) as _, i (i)}
      <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800"><div class="h-3 w-24 rounded shimmer"></div></header>
        <div class="px-3 py-3"><div class="h-[220px] rounded-md shimmer opacity-60"></div></div>
      </div>
    {/each}
  </div>
{:else if !hasData && !error}
  <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-12 text-center">
    <h2 class="text-base font-medium text-zinc-100">No GPU detected</h2>
    <p class="mt-1 text-sm text-zinc-500">The agent didn't find <code class="font-mono text-xs text-zinc-300">nvidia-smi</code> on this host.</p>
  </div>
{:else if hasData}
  <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">GPU utilization</div>
        <DownloadCsv host={hostId} metric="gpu_usage_pct" splitBy="gpu" {range} />
      </header>
      <div class="px-3 py-3"><MultiChart series={toSeries(usage)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="%" /></div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">VRAM utilization</div>
        <DownloadCsv host={hostId} metric="gpu_mem_used_pct" splitBy="gpu" {range} />
      </header>
      <div class="px-3 py-3"><MultiChart series={toSeries(memUsedPct)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="%" /></div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Temperature</div>
        <DownloadCsv host={hostId} metric="gpu_temp_c" splitBy="gpu" {range} />
      </header>
      <div class="px-3 py-3"><MultiChart series={toSeries(temp)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="°C" format={(v, e = 0) => `${v.toFixed(e)} °C`} /></div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Power draw</div>
        <DownloadCsv host={hostId} metric="gpu_power_w" splitBy="gpu" {range} />
      </header>
      <div class="px-3 py-3"><MultiChart series={toSeries(power)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="W" format={(v, e = 0) => `${v.toFixed(e)} W`} /></div>
    </section>
  </div>
{/if}
