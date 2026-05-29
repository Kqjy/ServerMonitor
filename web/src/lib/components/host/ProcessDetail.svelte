<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { api, type ProcessSeriesPoint } from '$lib/api';
  import MultiChart from '$lib/components/MultiChart.svelte';
  import { bytes } from '$lib/format';
  import { loadPresetWin, savePresetWin } from '$lib/time';

  let { hostId, pid, name = '', at = null }: { hostId: number; pid: number; name?: string; at?: number | null } = $props();

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
  let points = $state<ProcessSeriesPoint[]>([]);
  let fromMs = $state(Date.now() - 60 * 60 * 1000);
  let toMs = $state(Date.now());
  let ac: AbortController | null = null;
  let lastAnchor = 0;
  let lastWin: Win | null = null;
  let lastName = '';
  const minDeltaMs = 1_000;

  async function load() {
    const span = windows.find((w) => w.key === win)!.ms;
    const anchor = at ?? Date.now();
    if (win === lastWin && name === lastName && Math.abs(anchor - lastAnchor) < minDeltaMs) return;
    const windowChanged = win !== lastWin || name !== lastName;
    lastAnchor = anchor;
    lastWin = win;
    lastName = name;
    ac?.abort();
    ac = new AbortController();
    loading = true;
    const to = new Date(anchor);
    const from = new Date(anchor - span);
    if (windowChanged) {
      points = [];
      toMs = to.getTime();
      fromMs = from.getTime();
    }
    try {
      const resp = await api.processSeries(hostId, pid, {
        from: from.toISOString(),
        to: to.toISOString(),
        name: name || undefined,
        anchor: new Date(anchor).toISOString(),
        signal: ac.signal
      });
      points = resp.points;
      toMs = to.getTime();
      fromMs = from.getTime();
    } catch (e) {
      if ((e as Error).name !== 'AbortError') points = [];
    } finally {
      loading = false;
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
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
    <div>
      <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">RSS</div>
      <MultiChart
        series={memSeries}
        height={140}
        fill
        format={(v) => bytes(v)}
        {fromMs}
        {toMs}
        loading={loading && points.length === 0}
        emptyText="No samples in window" />
    </div>
  </div>
</div>
