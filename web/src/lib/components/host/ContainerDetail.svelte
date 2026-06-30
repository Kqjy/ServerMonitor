<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { api, type ContainerSeriesPoint } from '$lib/api';
  import MultiChart, { type ChartZoom } from '$lib/components/MultiChart.svelte';
  import { bytes } from '$lib/format';
  import { chooseStepSec, loadPresetWin, savePresetWin } from '$lib/time';

  let { hostId, cid, at = null, pinned = false, sampleIntervalS = 10 }: { hostId: number; cid: string; at?: number | null; pinned?: boolean; sampleIntervalS?: number } = $props();

  type Win = '15m' | '1h' | '6h' | '24h';
  const windows: { key: Win; ms: number; label: string }[] = [
    { key: '15m', ms: 15 * 60 * 1000, label: '15m' },
    { key: '1h',  ms: 60 * 60 * 1000, label: '1h' },
    { key: '6h',  ms: 6 * 60 * 60 * 1000, label: '6h' },
    { key: '24h', ms: 24 * 60 * 60 * 1000, label: '24h' }
  ];

  const WIN_STORAGE_KEY = 'sm_container_win';
  const allowedWins: readonly Win[] = ['15m', '1h', '6h', '24h'];
  let win = $state<Win>(loadPresetWin(WIN_STORAGE_KEY, allowedWins, '1h'));
  let prevWin: Win | null = null;
  $effect(() => {
    const v = win;
    if (prevWin !== null && prevWin !== v) savePresetWin(WIN_STORAGE_KEY, v);
    prevWin = v;
  });
  let loading = $state(false);
  let masking = $state(false);
  let points = $state<ContainerSeriesPoint[]>([]);
  let fromMs = $state(Date.now() - 60 * 60 * 1000);
  let toMs = $state(Date.now());
  let chartZoom = $state<ChartZoom>(null);
  let windowFromMs = Date.now() - 60 * 60 * 1000;
  let windowToMs = Date.now();
  let ac: AbortController | null = null;
  let lastAnchor = 0;
  let lastWin: Win | null = null;
  let lastAt: number | null = null;
  let lastPinned = false;
  let loadedStep = 0;
  let zoomFetched = false;
  let fetchGen = 0;
  const minDeltaMs = 1_000;

  async function runFetch(from: Date, to: Date, masked: boolean) {
    const gen = ++fetchGen;
    ac?.abort();
    ac = new AbortController();
    if (masked) masking = true;
    else loading = true;
    try {
      const resp = await api.containerSeries(hostId, cid, {
        from: from.toISOString(),
        to: to.toISOString(),
        signal: ac.signal
      });
      if (gen !== fetchGen) return;
      points = resp.points;
      loadedStep = resp.step_sec;
      zoomFetched = chartZoom !== null;
    } catch (e) {
      if (gen !== fetchGen) return;
      if (!masked && (e as Error).name !== 'AbortError') points = [];
    } finally {
      if (gen === fetchGen) {
        loading = false;
        masking = false;
      }
    }
  }

  async function load() {
    const span = windows.find((w) => w.key === win)!.ms;
    const anchor = at ?? Date.now();
    const winChanged = win !== lastWin;
    const pinChanged = pinned !== lastPinned;
    const pinValueChanged = pinned && at !== lastAt;
    const resetZoom = winChanged || pinChanged || pinValueChanged;
    if (chartZoom !== null && !resetZoom) return;
    if (!resetZoom && Math.abs(anchor - lastAnchor) < minDeltaMs) return;
    lastAnchor = anchor;
    lastWin = win;
    lastAt = at;
    lastPinned = pinned;
    const to = new Date(anchor);
    const from = new Date(anchor - span);
    windowFromMs = from.getTime();
    windowToMs = to.getTime();
    if (resetZoom) {
      points = [];
      chartZoom = null;
      toMs = to.getTime();
      fromMs = from.getTime();
    }
    await runFetch(from, to, false);
  }

  $effect(() => {
    void win;
    void at;
    void pinned;
    untrack(load);
  });

  onDestroy(() => {
    ac?.abort();
  });

  const cpuSeries = $derived([
    {
      label: 'CPU',
      color: 'oklch(0.7 0.18 240)',
      points: points.map((p) => ({ ts: p.ts, v: p.cpu_pct }))
    }
  ]);
  const memSeries = $derived([
    {
      label: 'Memory',
      color: 'oklch(0.78 0.16 162)',
      points: points.map((p) => ({ ts: p.ts, v: p.mem_used }))
    }
  ]);
  const netSeries = $derived([
    {
      label: 'Rx',
      color: 'oklch(0.78 0.16 195)',
      points: points.map((p) => ({ ts: p.ts, v: p.rx_rate }))
    },
    {
      label: 'Tx',
      color: 'oklch(0.83 0.18 85)',
      points: points.map((p) => ({ ts: p.ts, v: p.tx_rate }))
    }
  ]);

  const isZoomed = $derived(chartZoom !== null);
  function handleZoom(f: number, t: number) {
    chartZoom = { fromMs: f, toMs: t };
    fromMs = f;
    toMs = t;
    if (loadedStep > 0 && chooseStepSec(t - f, sampleIntervalS) < loadedStep) {
      runFetch(new Date(f), new Date(t), true);
    }
  }
  function handleReset() {
    chartZoom = null;
    fromMs = windowFromMs;
    toMs = windowToMs;
    if (zoomFetched) {
      runFetch(new Date(windowFromMs), new Date(windowToMs), true);
    }
  }
</script>

<div class="px-5 py-4 bg-zinc-950/60">
  <div class="flex items-center justify-between mb-3">
    <div class="text-[10px] uppercase tracking-wider text-zinc-500 font-mono">
      {cid} · last {win}
    </div>
    <div class="inline-flex rounded-md border border-zinc-800 overflow-hidden text-[11px]">
      {#each windows as w (w.key)}
        <button
          type="button"
          onclick={() => (win = w.key)}
          class={`px-2 py-1 transition-colors ${win === w.key ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-400 hover:bg-zinc-900'}`}>
          {w.label}
        </button>
      {/each}
    </div>
  </div>

  <div class="grid lg:grid-cols-3 gap-4">
    <div>
      <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">CPU</div>
      <MultiChart
        series={cpuSeries}
        unit="%"
        height={140}
        fill
        {fromMs}
        {toMs}
        yMinSpan={1}
        yClampMin={0}
        yMaxDigits={2}
        zoomed={isZoomed}
        {masking}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
    <div>
      <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">Memory</div>
      <MultiChart
        series={memSeries}
        height={140}
        fill
        format={(v, e = 0) => bytes(v, 1 + e)}
        {fromMs}
        {toMs}
        yClampMin={0}
        zoomed={isZoomed}
        {masking}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
    <div>
      <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">Net I/O</div>
      <MultiChart
        series={netSeries}
        height={140}
        format={(v, e = 0) => `${bytes(v, 1 + e)}/s`}
        {fromMs}
        {toMs}
        yClampMin={0}
        zoomed={isZoomed}
        {masking}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
  </div>
</div>
