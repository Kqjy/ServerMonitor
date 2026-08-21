<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesPoint, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, rangeMs, isPreset, chooseStepSec, type Range } from '$lib/time';
  import { groupPoints, groupSeries, incrementalFromMs, mergePoints, mergeSeries } from '$lib/series';
  import { pct, bytes, dur } from '$lib/format';
  import { subscribeHost, appendLive } from '$lib/sse';
  import MultiChart, { type ChartZoom } from '$lib/components/MultiChart.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range, sampleIntervalS = 10 }: { hostId: number; range: Range; sampleIntervalS?: number } = $props();

  let cpu = $state<SeriesPoint[]>([]);
  let cpuUser = $state<SeriesPoint[]>([]);
  let cpuSystem = $state<SeriesPoint[]>([]);
  let cpuIowait = $state<SeriesPoint[]>([]);
  let cpuSteal = $state<SeriesPoint[]>([]);
  let cpuCores = $state<SeriesEntry[]>([]);
  let load1Series = $state<SeriesPoint[]>([]);
  let load5Series = $state<SeriesPoint[]>([]);
  let load15Series = $state<SeriesPoint[]>([]);
  let memPct = $state<SeriesPoint[]>([]);
  let memUsed = $state(0);
  let memTotal = $state(0);
  let upSec = $state(0);
  let load1 = $state(0);
  let load5 = $state(0);
  let load15 = $state(0);
  let iowaitNow = $state(0);
  let stealNow = $state(0);
  let freqNow = $state(0);
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
  let unsub: (() => void) | null = null;
  let prevRange: Range | null = null;

  const historySpecs = [
    { metric: 'cpu_total_pct' },
    { metric: 'cpu_user_pct' },
    { metric: 'cpu_system_pct' },
    { metric: 'cpu_iowait_pct' },
    { metric: 'cpu_steal_pct' },
    { metric: 'cpu_core_pct', splitBy: 'core' },
    { metric: 'load_avg_1' },
    { metric: 'load_avg_5' },
    { metric: 'load_avg_15' },
    { metric: 'mem_used_pct' }
  ];
  const liveSpecs = [
    { metric: 'mem_used' },
    { metric: 'mem_total' },
    { metric: 'load_avg_1' },
    { metric: 'load_avg_5' },
    { metric: 'load_avg_15' },
    { metric: 'uptime_sec' },
    { metric: 'cpu_iowait_pct' },
    { metric: 'cpu_steal_pct' },
    { metric: 'cpu_freq_mhz' }
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
      const nextCpu = groupPoints(history, 'cpu_total_pct');
      const nextCPUUser = groupPoints(history, 'cpu_user_pct');
      const nextCPUSystem = groupPoints(history, 'cpu_system_pct');
      const nextCPUIOWait = groupPoints(history, 'cpu_iowait_pct');
      const nextCPUSteal = groupPoints(history, 'cpu_steal_pct');
      const nextCores = groupSeries(history, 'cpu_core_pct');
      const nextLoad1 = groupPoints(history, 'load_avg_1');
      const nextLoad5 = groupPoints(history, 'load_avg_5');
      const nextLoad15 = groupPoints(history, 'load_avg_15');
      const nextMemPct = groupPoints(history, 'mem_used_pct');
      if (incremental) {
        cpu = mergePoints(cpu, nextCpu, fromMs, toMs, replaceFromMs);
        cpuUser = mergePoints(cpuUser, nextCPUUser, fromMs, toMs, replaceFromMs);
        cpuSystem = mergePoints(cpuSystem, nextCPUSystem, fromMs, toMs, replaceFromMs);
        cpuIowait = mergePoints(cpuIowait, nextCPUIOWait, fromMs, toMs, replaceFromMs);
        cpuSteal = mergePoints(cpuSteal, nextCPUSteal, fromMs, toMs, replaceFromMs);
        cpuCores = mergeSeries(cpuCores, nextCores, fromMs, toMs, 'core', replaceFromMs);
        load1Series = mergePoints(load1Series, nextLoad1, fromMs, toMs, replaceFromMs);
        load5Series = mergePoints(load5Series, nextLoad5, fromMs, toMs, replaceFromMs);
        load15Series = mergePoints(load15Series, nextLoad15, fromMs, toMs, replaceFromMs);
        memPct = mergePoints(memPct, nextMemPct, fromMs, toMs, replaceFromMs);
      } else {
        cpu = nextCpu;
        cpuUser = nextCPUUser;
        cpuSystem = nextCPUSystem;
        cpuIowait = nextCPUIOWait;
        cpuSteal = nextCPUSteal;
        cpuCores = nextCores;
        load1Series = nextLoad1;
        load5Series = nextLoad5;
        load15Series = nextLoad15;
        memPct = nextMemPct;
      }
      loadedStep = history.step_sec;
      zoomFetched = zoomed !== null;
      cpuCores = cpuCores.slice().sort((a, b2) => {
        const ai = parseInt(a.labels?.core ?? '0', 10);
        const bi = parseInt(b2.labels?.core ?? '0', 10);
        return ai - bi;
      });
      memUsed = groupPoints(live, 'mem_used').at(-1)?.v ?? 0;
      memTotal = groupPoints(live, 'mem_total').at(-1)?.v ?? 0;
      load1 = groupPoints(live, 'load_avg_1').at(-1)?.v ?? 0;
      load5 = groupPoints(live, 'load_avg_5').at(-1)?.v ?? 0;
      load15 = groupPoints(live, 'load_avg_15').at(-1)?.v ?? 0;
      upSec = groupPoints(live, 'uptime_sec').at(-1)?.v ?? 0;
      iowaitNow = groupPoints(live, 'cpu_iowait_pct').at(-1)?.v ?? 0;
      stealNow = groupPoints(live, 'cpu_steal_pct').at(-1)?.v ?? 0;
      freqNow = groupPoints(live, 'cpu_freq_mhz').at(-1)?.v ?? 0;
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
      cpu = [];
      cpuUser = [];
      cpuSystem = [];
      cpuIowait = [];
      cpuSteal = [];
      cpuCores = [];
      load1Series = [];
      load5Series = [];
      load15Series = [];
      memPct = [];
    }
    prevRange = current;
  });

  $effect(() => {
    void range;
    untrack(() => refresh());
  });

  onMount(() => {
    timer = setInterval(() => { if (chartZoom === null) void refresh(true); }, 30_000);
    unsub = subscribeHost(
      hostId,
      ['cpu_total_pct', 'cpu_user_pct', 'cpu_system_pct', 'cpu_iowait_pct', 'cpu_steal_pct', 'cpu_core_pct', 'cpu_freq_mhz', 'mem_used_pct', 'mem_used', 'load_avg_1', 'load_avg_5', 'load_avg_15', 'uptime_sec'],
      (points) => {
        if (chartZoom !== null) return;
        if (!isPreset(range)) return;
        if (loading) return;
        const win = rangeMs(range);
        const now = Date.now();
        toMs = now;
        fromMs = now - win;
        for (const p of points) {
          if (p.metric === 'cpu_total_pct') cpu = appendLive(cpu, { ts: p.ts, v: p.v }, win);
          else if (p.metric === 'cpu_user_pct') cpuUser = appendLive(cpuUser, { ts: p.ts, v: p.v }, win);
          else if (p.metric === 'cpu_system_pct') cpuSystem = appendLive(cpuSystem, { ts: p.ts, v: p.v }, win);
          else if (p.metric === 'cpu_iowait_pct') { cpuIowait = appendLive(cpuIowait, { ts: p.ts, v: p.v }, win); iowaitNow = p.v; }
          else if (p.metric === 'cpu_steal_pct') { cpuSteal = appendLive(cpuSteal, { ts: p.ts, v: p.v }, win); stealNow = p.v; }
          else if (p.metric === 'cpu_core_pct') {
            const core = p.labels?.core;
            if (core != null) {
              const idx = cpuCores.findIndex((e) => e.labels?.core === core);
              if (idx >= 0) {
                const next = cpuCores.slice();
                next[idx] = { labels: next[idx].labels, points: appendLive(next[idx].points, { ts: p.ts, v: p.v }, win) };
                cpuCores = next;
              }
            }
          }
          else if (p.metric === 'cpu_freq_mhz') freqNow = p.v;
          else if (p.metric === 'mem_used_pct') memPct = appendLive(memPct, { ts: p.ts, v: p.v }, win);
          else if (p.metric === 'mem_used') memUsed = p.v;
          else if (p.metric === 'load_avg_1') { load1 = p.v; load1Series = appendLive(load1Series, { ts: p.ts, v: p.v }, win); }
          else if (p.metric === 'load_avg_5') { load5 = p.v; load5Series = appendLive(load5Series, { ts: p.ts, v: p.v }, win); }
          else if (p.metric === 'load_avg_15') { load15 = p.v; load15Series = appendLive(load15Series, { ts: p.ts, v: p.v }, win); }
          else if (p.metric === 'uptime_sec') upSec = p.v;
        }
      }
    );
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    inflight?.abort();
    unsub?.();
  });

  const isZoomed = $derived(chartZoom !== null);
  const cpuNow = $derived(cpu.at(-1)?.v ?? 0);
  const cpuTone = $derived(cpuNow > 90 ? 'bad' : cpuNow > 70 ? 'warn' : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const memUsedPctNow = $derived(memTotal ? (memUsed / memTotal) * 100 : 0);
  const memTone = $derived(memUsedPctNow > 90 ? 'bad' : memUsedPctNow > 75 ? 'warn' : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const iowaitTone = $derived(iowaitNow > 20 ? 'bad' : iowaitNow > 5 ? 'warn' : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const stealTone = $derived(stealNow > 10 ? 'bad' : stealNow > 2 ? 'warn' : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const hasLoad = $derived(load1 + load5 + load15 > 0);
  const hasFreq = $derived(freqNow > 0);
  const hasCoreData = $derived(cpuCores.some((e) => e.points.length > 0));
  const coreCount = $derived(cpuCores.length);
  const hasLoadSeries = $derived(load1Series.length + load5Series.length + load15Series.length > 0);
  let selectedCore = $state<string | null>(null);
  const selectedCoreEntry = $derived(
    selectedCore == null ? null : cpuCores.find((e) => (e.labels?.core ?? '') === selectedCore) ?? null
  );
  const selectedCoreNow = $derived(selectedCoreEntry?.points.at(-1)?.v ?? 0);

  $effect(() => {
    if (selectedCore != null && !cpuCores.some((e) => (e.labels?.core ?? '') === selectedCore)) {
      selectedCore = null;
    }
  });

  function coreColor(pctNow: number): string {
    if (pctNow >= 90) return 'oklch(0.7 0.21 22)';
    if (pctNow >= 70) return 'oklch(0.83 0.18 85)';
    return 'oklch(0.78 0.16 162)';
  }
  function toggleCore(core: string) {
    selectedCore = selectedCore === core ? null : core;
  }
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
      Failed to load overview data: {error}
    </div>
  {/if}
  <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
    <StatCard label="CPU" value={pct(cpuNow, 2)} tone={cpuTone} {loading} />
    <StatCard label="Memory" value={memTotal ? pct(memUsedPctNow, 2) : '—'} sub={memTotal ? `${bytes(memUsed)} / ${bytes(memTotal)}` : ''} tone={memTone} {loading} />
    <StatCard label="Load avg" value={hasLoad ? load1.toFixed(2) : '—'} sub={hasLoad ? `${load5.toFixed(2)} · ${load15.toFixed(2)} (5m · 15m)` : ''} {loading} />
    <StatCard label="Uptime" value={upSec ? dur(upSec) : '—'} sub={hasFreq ? `${freqNow.toFixed(0)} MHz` : ''} {loading} />
  </div>

  <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
    <StatCard label="CPU iowait" value={pct(iowaitNow, 2)} tone={iowaitTone} {loading} />
    <StatCard label="CPU steal" value={pct(stealNow, 2)} tone={stealTone} {loading} />
    <StatCard label="CPU user" value={pct(cpuUser.at(-1)?.v ?? 0, 2)} {loading} />
    <StatCard label="CPU system" value={pct(cpuSystem.at(-1)?.v ?? 0, 2)} {loading} />
  </div>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">CPU total</div>
      <div class="flex items-center gap-3">
        <div class="text-xs text-zinc-500 numeric">{pct(cpuNow, 2)}</div>
        <DownloadCsv host={hostId} metric="cpu_total_pct" {range} />
      </div>
    </header>
    <div class="px-3 py-3">
      <MultiChart
        series={[{ label: 'CPU %', points: cpu }]}
        {fromMs}
        {toMs}
        zoomed={isZoomed} {masking}
        {loading}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="%"
        yMinSpan={2}
        yClampMin={0}
        yMaxDigits={2}
        fill />
    </div>
  </section>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">CPU per core</div>
      <div class="flex items-center gap-3">
        <div class="text-xs text-zinc-500 numeric">{coreCount} core{coreCount === 1 ? '' : 's'}</div>
        <DownloadCsv host={hostId} metric="cpu_core_pct" splitBy="core" {range} />
      </div>
    </header>
    <div class="p-3">
      {#if !hasCoreData}
        <div class="px-3 py-10 text-center text-[11px] uppercase tracking-wider text-zinc-500">
          {loading ? 'Loading…' : 'No per-core data'}
        </div>
      {:else}
        <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6 xl:grid-cols-8 gap-2">
          {#each cpuCores as core, i (core.labels?.core ?? i)}
            {@const v = core.points.at(-1)?.v ?? 0}
            {@const col = coreColor(v)}
            {@const id = core.labels?.core ?? String(i)}
            {@const sel = selectedCore === id}
            <button
              type="button"
              onclick={() => toggleCore(id)}
              aria-pressed={sel}
              class="text-left rounded-lg border bg-zinc-950/40 px-2.5 py-2 transition-colors {sel ? 'border-zinc-500 bg-zinc-900/60' : 'border-zinc-800 hover:border-zinc-700'}">
              <div class="flex items-baseline justify-between gap-2">
                <div class="text-[10px] uppercase tracking-wider {sel ? 'text-zinc-300' : 'text-zinc-500'}">Core {id}</div>
                <div class="text-xs numeric" style="color: {col}">{v.toFixed(1)}%</div>
              </div>
              <div class="mt-1.5">
                <Sparkline points={core.points} color={col} height={28} yMin={0} yMax={100} fill />
              </div>
            </button>
          {/each}
        </div>
      {/if}
    </div>
    {#if selectedCoreEntry}
      <div class="border-t border-zinc-800 px-3 py-3">
        <div class="flex items-center justify-between mb-2 px-2">
          <div class="flex items-baseline gap-3">
            <div class="text-xs uppercase tracking-wider text-zinc-400">Core {selectedCore} detail</div>
            <div class="text-xs numeric" style="color: {coreColor(selectedCoreNow)}">{selectedCoreNow.toFixed(2)}%</div>
          </div>
          <button
            type="button"
            onclick={() => (selectedCore = null)}
            class="inline-flex items-center gap-1.5 px-2 py-1 rounded-md text-[10px] uppercase tracking-wider text-zinc-300 border border-zinc-700 bg-zinc-900/80 hover:bg-zinc-800 transition-colors">
            Close
          </button>
        </div>
        {#key selectedCore}
          <MultiChart
            series={[{ label: `core ${selectedCore}`, color: coreColor(selectedCoreNow), points: selectedCoreEntry.points }]}
            {fromMs}
            {toMs}
            zoomed={isZoomed} {masking}
            {loading}
            onZoom={handleZoom}
            onResetZoom={handleReset}
            unit="%"
            yMinSpan={2}
            yClampMin={0}
            yMaxDigits={2}
            fill />
        {/key}
      </div>
    {/if}
  </section>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">CPU breakdown</header>
    <div class="px-3 py-3">
      <MultiChart
        series={[
          { label: 'user', color: 'oklch(0.78 0.16 162)', points: cpuUser },
          { label: 'system', color: 'oklch(0.7 0.18 240)', points: cpuSystem },
          { label: 'iowait', color: 'oklch(0.83 0.18 85)', points: cpuIowait },
          { label: 'steal', color: 'oklch(0.7 0.21 22)', points: cpuSteal }
        ]}
        {fromMs}
        {toMs}
        zoomed={isZoomed} {masking}
        {loading}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="%"
        yMinSpan={1}
        yClampMin={0}
        yMaxDigits={2} />
    </div>
  </section>

  {#if hasLoadSeries}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Load average</div>
        <div class="text-xs text-zinc-500 numeric">{load1.toFixed(2)} · {load5.toFixed(2)} · {load15.toFixed(2)}</div>
      </header>
      <div class="px-3 py-3">
        <MultiChart
          series={[
            { label: '1m', color: 'oklch(0.78 0.16 162)', points: load1Series },
            { label: '5m', color: 'oklch(0.83 0.18 85)', points: load5Series },
            { label: '15m', color: 'oklch(0.7 0.18 240)', points: load15Series }
          ]}
          {fromMs}
          {toMs}
          zoomed={isZoomed} {masking}
          {loading}
          onZoom={handleZoom}
          onResetZoom={handleReset}
          yMinSpan={0.5}
          yClampMin={0}
          yMaxDigits={2} />
      </div>
    </section>
  {/if}

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Memory utilization</div>
      <div class="flex items-center gap-3">
        <div class="text-xs text-zinc-500 numeric">{pct(memPct.at(-1)?.v ?? 0, 2)}</div>
        <DownloadCsv host={hostId} metric="mem_used_pct" {range} />
      </div>
    </header>
    <div class="px-3 py-3">
      <MultiChart
        series={[{ label: 'Memory %', color: 'oklch(0.7 0.18 240)', points: memPct }]}
        {fromMs}
        {toMs}
        zoomed={isZoomed} {masking}
        {loading}
        onZoom={handleZoom}
        onResetZoom={handleReset}
        unit="%"
        yMinSpan={2}
        yClampMin={0}
        yMaxDigits={2}
        fill />
    </div>
  </section>
</div>
