<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { api, type ProcessSeriesPoint } from '$lib/api';
  import MultiChart from '$lib/components/MultiChart.svelte';
  import { bytes } from '$lib/format';
  import { chooseStepSec, loadPresetWin, savePresetWin } from '$lib/time';

  let { hostId, pid, name = '', at = null, live = true, sampleIntervalS = 10 }: { hostId: number; pid: number; name?: string; at?: number | null; live?: boolean; sampleIntervalS?: number } = $props();

  type Win = '15m' | '1h' | '6h' | '24h';
  const windows: { key: Win; ms: number; label: string }[] = [
    { key: '15m', ms: 15 * 60 * 1000, label: '15m' },
    { key: '1h',  ms: 60 * 60 * 1000, label: '1h' },
    { key: '6h',  ms: 6 * 60 * 60 * 1000, label: '6h' },
    { key: '24h', ms: 24 * 60 * 60 * 1000, label: '24h' }
  ];

  const WIN_STORAGE_KEY = 'sm_process_win';
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
  let points = $state<ProcessSeriesPoint[]>([]);
  let fromMs = $state(Date.now() - 60 * 60 * 1000);
  let toMs = $state(Date.now());
  let ac: AbortController | null = null;
  let lastAnchor = 0;
  let lastWin: Win | null = null;
  let lastName = '';
  const minDeltaMs = 1_000;
  let chartZoom = $state<{ fromMs: number; toMs: number } | null>(null);
  let loadedStep = 0;
  let zoomFetched = false;
  let fetchGen = 0;
  const isZoomed = $derived(chartZoom !== null);

  async function runFetch(from: Date, to: Date, anchorMs: number, masked: boolean) {
    const gen = ++fetchGen;
    ac?.abort();
    ac = new AbortController();
    if (masked) masking = true;
    else loading = true;
    try {
      const resp = await api.processSeries(hostId, pid, {
        from: from.toISOString(),
        to: to.toISOString(),
        name: name || undefined,
        anchor: new Date(anchorMs).toISOString(),
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
    if (win === lastWin && name === lastName && Math.abs(anchor - lastAnchor) < minDeltaMs) return;
    const windowChanged = win !== lastWin || name !== lastName;
    if (chartZoom !== null) {
      if (live && !windowChanged) return;
      chartZoom = null;
    }
    lastAnchor = anchor;
    lastWin = win;
    lastName = name;
    const to = new Date(anchor);
    const from = new Date(anchor - span);
    if (windowChanged) points = [];
    toMs = to.getTime();
    fromMs = from.getTime();
    await runFetch(from, to, anchor, false);
  }

  function handleZoom(f: number, t: number) {
    chartZoom = { fromMs: f, toMs: t };
    fromMs = f;
    toMs = t;
    if (loadedStep > 0 && chooseStepSec(t - f, sampleIntervalS) < loadedStep) {
      runFetch(new Date(f), new Date(t), t, true);
    }
  }
  function handleReset() {
    chartZoom = null;
    const span = windows.find((w) => w.key === win)!.ms;
    const anchor = lastAnchor || Date.now();
    toMs = anchor;
    fromMs = anchor - span;
    if (zoomFetched) {
      runFetch(new Date(anchor - span), new Date(anchor), anchor, true);
    }
  }

  $effect(() => {
    void win;
    void at;
    void name;
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
      label: 'RSS',
      color: 'oklch(0.78 0.16 162)',
      points: points.map((p) => ({ ts: p.ts, v: p.mem_rss }))
    }
  ]);
</script>

<div class="px-5 py-4 bg-zinc-950/60">
  <div class="flex items-center justify-between mb-3">
    <div class="text-[10px] uppercase tracking-wider text-zinc-500">
      PID {pid} · last {win}
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

  <div class="grid md:grid-cols-2 gap-4">
    <div>
      <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">CPU</div>
      <MultiChart
        series={cpuSeries}
        unit="%"
        height={140}
        fill
        {fromMs}
        {toMs}
        zoomed={isZoomed}
        {masking}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
    <div>
      <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">RSS</div>
      <MultiChart
        series={memSeries}
        height={140}
        fill
        format={(v, e = 0) => bytes(v, 1 + e)}
        {fromMs}
        {toMs}
        zoomed={isZoomed}
        {masking}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
  </div>
</div>
