<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, type Range } from '$lib/time';
  import { bytes } from '$lib/format';
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
  let refreshGen = 0;
  let inflight: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let prevRange: Range | null = null;

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: `GPU ${e.labels.gpu}${e.labels.name ? ' · ' + e.labels.name : ''}`,
      points: e.points
    }));
  }

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    const zoomed = chartZoom;
    let from: string;
    let to: string | undefined;
    if (zoomed) {
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
      const [u, mp, t, p] = await Promise.all([
        api.seriesMulti({ host: hostId, metric: 'gpu_usage_pct', from, to, splitBy: 'gpu', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'gpu_mem_used_pct', from, to, splitBy: 'gpu', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'gpu_temp_c', from, to, splitBy: 'gpu', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'gpu_power_w', from, to, splitBy: 'gpu', signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      loadedStep = u.step_sec;
      zoomFetched = zoomed !== null;
      usage = u.series;
      memUsedPct = mp.series;
      temp = t.series;
      power = p.series;
      loading = false;
      masking = false;
    } catch (e) {
      if (gen !== refreshGen) return;
      if ((e as { name?: string })?.name === 'AbortError') return;
      loading = false;
      masking = false;
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
    timer = setInterval(() => { if (chartZoom === null) refresh(); }, 10_000);
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
  void bytes;
</script>

{#if loading}
  <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
    {#each Array(4) as _, i (i)}
      <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800"><div class="h-3 w-24 rounded shimmer"></div></header>
        <div class="px-3 py-3"><div class="h-[220px] rounded-md shimmer opacity-60"></div></div>
      </div>
    {/each}
  </div>
{:else if !hasData}
  <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-12 text-center">
    <h2 class="text-base font-medium text-zinc-100">No GPU detected</h2>
    <p class="mt-1 text-sm text-zinc-500">The agent didn't find <code class="font-mono text-xs text-zinc-300">nvidia-smi</code> on this host.</p>
  </div>
{:else}
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
      <div class="px-3 py-3"><MultiChart series={toSeries(temp)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="°C" format={(v) => `${v.toFixed(0)} °C`} /></div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Power draw</div>
        <DownloadCsv host={hostId} metric="gpu_power_w" splitBy="gpu" {range} />
      </header>
      <div class="px-3 py-3"><MultiChart series={toSeries(power)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="W" format={(v) => `${v.toFixed(0)} W`} /></div>
    </section>
  </div>
{/if}
