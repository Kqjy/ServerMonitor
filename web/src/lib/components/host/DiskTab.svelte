<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry, type SeriesPoint, type CollectorStatus } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, type Range, type CustomRange } from '$lib/time';
  import { bytes, pct, dur, timeAgo } from '$lib/format';
  import { modalFocus } from '$lib/modal';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let {
    hostId,
    range,
    sampleIntervalS = 10,
    enabledCollectors = [],
    collectorStatus = {}
  }: {
    hostId: number;
    range: Range;
    sampleIntervalS?: number;
    enabledCollectors?: string[];
    collectorStatus?: Record<string, CollectorStatus>;
  } = $props();

  const smartEnabled = $derived(enabledCollectors.includes('smart'));
  const smartStatus = $derived(collectorStatus.smart);

  type SmartRow = {
    device: string;
    slot: string | null;
    model: string | null;
    healthy: number | null;
    powerOnHours: number | null;
    realloc: number | null;
    pending: number | null;
    tempC: number | null;
    written: number | null;
    read: number | null;
    percentUsed: number | null;
    mediaErrors: number | null;
    unsafeShutdowns: number | null;
  };

  const MEDIA_HISTORY_DAYS = 90;

  const smartCounterMeta: Record<string, { label: string; metric: string }> = {
    mediaErrors: { label: 'Media errors', metric: 'smart_media_errors' },
    realloc: { label: 'Reallocated sectors', metric: 'smart_realloc_sectors' },
    pending: { label: 'Pending sectors', metric: 'smart_pending_sectors' }
  };

  type CounterChange = { ts: string; from: number; to: number };

  type RaidRow = {
    array: string;
    type: string | null;
    degraded: number;
    syncPct: number | null;
  };

  let read = $state<SeriesEntry[]>([]);
  let write = $state<SeriesEntry[]>([]);
  let readOps = $state<SeriesEntry[]>([]);
  let writeOps = $state<SeriesEntry[]>([]);
  let fs = $state<{ mount: string; fstype: string; used: number; total: number; pct: number }[]>([]);
  let overall = $state<{ total: number; used: number; pct: number } | null>(null);
  let fsUsedPct = $state<SeriesEntry[]>([]);
  let smartTemps = $state<SeriesEntry[]>([]);
  let smartWritten = $state<SeriesEntry[]>([]);
  let smartRead = $state<SeriesEntry[]>([]);
  let smart = $state<SmartRow[]>([]);
  let raid = $state<RaidRow[]>([]);
  let smartNewestAt = $state<number | null>(null);
  let freshnessNow = $state(Date.now());
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
  let detailOpen = $state(false);
  let detailDevice = $state('');
  let detailModel = $state<string | null>(null);
  let detailSlot = $state<string | null>(null);
  let detailAttr = $state('');
  let detailWin = $state<CustomRange>({ fromMs: 0, toMs: 0 });
  let detailPoints = $state<SeriesPoint[]>([]);
  let detailLoading = $state(false);
  let detailError = $state<string | null>(null);
  let detailGen = 0;
  let detailInflight: AbortController | null = null;

  const smartFreshness = $derived.by(() => {
    if (smartNewestAt === null) return null;
    return {
      text: `as of ${timeAgo(new Date(smartNewestAt).toISOString())}`,
      stale: freshnessNow - smartNewestAt > 15 * 60_000
    };
  });

  const detailAnalysis = $derived.by(() => {
    const pts = detailPoints;
    if (pts.length === 0) return null;
    const firstVal = Math.round(pts[0].v);
    const current = Math.round(pts[pts.length - 1].v);
    let runningMax = firstVal;
    const changes: CounterChange[] = [];
    for (let i = 1; i < pts.length; i++) {
      const v = Math.round(pts[i].v);
      if (v > runningMax) {
        changes.push({ ts: pts[i].ts, from: runningMax, to: v });
        runningMax = v;
      }
    }
    return { firstTs: pts[0].ts, firstVal, current, changes };
  });

  const absDate = (ts: string) => new Date(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: e.labels.device ?? e.labels.mount ?? Object.values(e.labels).join(' '),
      points: e.points
    }));
  }

  async function openDetail(row: SmartRow, attrKey: string) {
    const meta = smartCounterMeta[attrKey];
    if (!meta) return;
    const gen = ++detailGen;
    detailInflight?.abort();
    const ac = new AbortController();
    detailInflight = ac;
    detailDevice = row.device;
    detailModel = row.model;
    detailSlot = row.slot;
    detailAttr = attrKey;
    const toMs = Date.now();
    const fromMs = toMs - MEDIA_HISTORY_DAYS * 86_400_000;
    detailWin = { fromMs, toMs };
    detailOpen = true;
    detailLoading = true;
    detailError = null;
    detailPoints = [];
    try {
      const resp = await api.series({
        host: hostId,
        metric: meta.metric,
        from: rangeToFrom(detailWin),
        to: rangeToTo(detailWin),
        labels: { device: row.device },
        signal: ac.signal
      });
      if (gen !== detailGen) return;
      detailPoints = resp.points;
    } catch (e) {
      if (gen !== detailGen) return;
      if ((e as { name?: string })?.name === 'AbortError') return;
      detailError = (e as Error).message;
    } finally {
      if (gen === detailGen) detailLoading = false;
    }
  }

  function closeDetail() {
    detailGen++;
    detailInflight?.abort();
    detailOpen = false;
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
      const [r, wr, rOps, wOps, fsTotal, fsUsed, fsPct, overallTotal, overallUsed, overallPct, sTemp, sHealthy, sHours, sRealloc, sPending, sWritten, sRead, sUsed, sMedia, sUnsafe, raidDegraded, raidSync, fsUsage] = await Promise.all([
        api.seriesMulti({ host: hostId, metric: 'disk_read_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_write_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_read_ops', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_write_ops', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_total', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used_pct', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.series({ host: hostId, metric: 'fs_overall_total', from: '-2m', step: 30, signal: ac.signal }),
        api.series({ host: hostId, metric: 'fs_overall_used', from: '-2m', step: 30, signal: ac.signal }),
        api.series({ host: hostId, metric: 'fs_overall_used_pct', from: '-2m', step: 30, signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_temp_c', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_healthy', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_power_on_hours', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_realloc_sectors', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_pending_sectors', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_data_written_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_data_read_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_percent_used', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_media_errors', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_unsafe_shutdowns', from: '-20m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'raid_degraded', from: '-20m', step: 30, splitBy: 'array', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'raid_sync_pct', from: '-20m', step: 30, splitBy: 'array', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used_pct', from, to, splitBy: 'mount', signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      loadedStep = r.step_sec;
      zoomFetched = zoomed !== null;
      read = r.series;
      write = wr.series;
      readOps = rOps.series;
      writeOps = wOps.series;
      fsUsedPct = fsUsage.series;

      const byMount: Record<string, { mount: string; fstype: string; used: number; total: number; pct: number }> = {};
      for (const e of fsTotal.series) {
        const m = e.labels.mount;
        if (!m) continue;
        byMount[m] = { mount: m, fstype: e.labels.fstype ?? '', used: 0, total: e.points.at(-1)?.v ?? 0, pct: 0 };
      }
      for (const e of fsUsed.series) {
        const m = e.labels.mount;
        if (!m || !byMount[m]) continue;
        byMount[m].used = e.points.at(-1)?.v ?? 0;
      }
      for (const e of fsPct.series) {
        const m = e.labels.mount;
        if (!m || !byMount[m]) continue;
        byMount[m].pct = e.points.at(-1)?.v ?? 0;
      }
      fs = Object.values(byMount).sort((a, b) => b.pct - a.pct);
      const overallTotalValue = overallTotal.points.at(-1)?.v;
      const overallUsedValue = overallUsed.points.at(-1)?.v;
      const overallPctValue = overallPct.points.at(-1)?.v;
      overall = overallTotalValue !== undefined && overallUsedValue !== undefined && overallPctValue !== undefined
        ? { total: overallTotalValue, used: overallUsedValue, pct: overallPctValue }
        : null;

      const byDev: Record<string, SmartRow> = {};
      const fill = (entries: SeriesEntry[], assign: (row: SmartRow, v: number) => void) => {
        for (const e of entries) {
          const d = e.labels.device;
          if (!d) continue;
          if (!byDev[d]) byDev[d] = { device: d, slot: null, model: null, healthy: null, powerOnHours: null, realloc: null, pending: null, tempC: null, written: null, read: null, percentUsed: null, mediaErrors: null, unsafeShutdowns: null };
          const row = byDev[d];
          if (e.labels.model && !row.model) row.model = e.labels.model;
          if (e.labels.slot && !row.slot) row.slot = e.labels.slot;
          const v = e.points.at(-1)?.v;
          if (v !== undefined) assign(row, v);
        }
      };
      fill(sHealthy.series, (r, v) => (r.healthy = v));
      fill(sHours.series, (r, v) => (r.powerOnHours = v));
      fill(sRealloc.series, (r, v) => (r.realloc = v));
      fill(sPending.series, (r, v) => (r.pending = v));
      fill(sTemp.series, (r, v) => (r.tempC = v));
      fill(sWritten.series, (r, v) => (r.written = v));
      fill(sRead.series, (r, v) => (r.read = v));
      fill(sUsed.series, (r, v) => (r.percentUsed = v));
      fill(sMedia.series, (r, v) => (r.mediaErrors = v));
      fill(sUnsafe.series, (r, v) => (r.unsafeShutdowns = v));
      smart = Object.values(byDev).sort((a, b) => a.device.localeCompare(b.device, undefined, { numeric: true }));
      smartTemps = sTemp.series;
      smartWritten = sWritten.series;
      smartRead = sRead.series;
      const smartEntries = [sTemp, sHealthy, sHours, sRealloc, sPending, sWritten, sRead, sUsed, sMedia, sUnsafe].flatMap((response) => response.series);
      let newest = 0;
      for (const entry of smartEntries) {
        for (const point of entry.points) newest = Math.max(newest, new Date(point.ts).getTime());
      }
      smartNewestAt = newest > 0 ? newest : null;
      freshnessNow = Date.now();

      const byArray: Record<string, RaidRow> = {};
      for (const e of raidDegraded.series) {
        const array = e.labels.array;
        const degraded = e.points.at(-1)?.v;
        if (!array || degraded === undefined) continue;
        byArray[array] = { array, type: e.labels.type ?? null, degraded, syncPct: null };
      }
      for (const e of raidSync.series) {
        const array = e.labels.array;
        const syncPct = e.points.at(-1)?.v;
        if (!array || syncPct === undefined) continue;
        if (!byArray[array]) byArray[array] = { array, type: e.labels.type ?? null, degraded: 0, syncPct: null };
        if (e.labels.type && !byArray[array].type) byArray[array].type = e.labels.type;
        byArray[array].syncPct = syncPct;
      }
      raid = Object.values(byArray).sort((a, b) => a.array.localeCompare(b.array, undefined, { numeric: true }));

      error = null;
      loading = false;
      masking = false;
    } catch (e) {
      if (gen !== refreshGen) return;
      if ((e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
      loading = false;
      masking = false;
    }
  }

  $effect(() => {
    const current = range;
    if (prevRange !== null && !rangeEquals(current, prevRange)) {
      chartZoom = null;
      loading = true;
      read = [];
      write = [];
      readOps = [];
      writeOps = [];
      fsUsedPct = [];
      smartTemps = [];
      smartWritten = [];
      smartRead = [];
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
    detailInflight?.abort();
  });

  const isZoomed = $derived(chartZoom !== null);
  const smartHasSlots = $derived(smart.some((row) => row.slot !== null));
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
      Failed to load disk data: {error}
    </div>
  {/if}
  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Read throughput</div>
      <DownloadCsv host={hostId} metric="disk_read_bytes" splitBy="device" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(read)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v, e = 0) => `${bytes(v, e)}/s`} />
    </div>
  </section>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Write throughput</div>
      <DownloadCsv host={hostId} metric="disk_write_bytes" splitBy="device" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(write)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v, e = 0) => `${bytes(v, e)}/s`} />
    </div>
  </section>

  <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Read IOPS</div>
        <DownloadCsv host={hostId} metric="disk_read_ops" splitBy="device" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(readOps)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="ops/s" format={(v, e = 0) => `${v.toFixed(e)}/s`} />
      </div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Write IOPS</div>
        <DownloadCsv host={hostId} metric="disk_write_ops" splitBy="device" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(writeOps)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="ops/s" format={(v, e = 0) => `${v.toFixed(e)}/s`} />
      </div>
    </section>
  </div>

  {#if fsUsedPct.length > 0}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Filesystem usage</div>
        <DownloadCsv host={hostId} metric="fs_used_pct" splitBy="mount" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(fsUsedPct)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="%" format={(v, e = 0) => pct(v, 1 + e)} />
      </div>
    </section>
  {/if}

  {#if overall}
    <div class="text-xs text-zinc-400">
      Overall <span class="numeric tabular-nums text-zinc-300">{pct(overall.pct, 1)}</span> — <span class="numeric tabular-nums text-zinc-300">{bytes(overall.used)}</span> of <span class="numeric tabular-nums text-zinc-300">{bytes(overall.total)}</span> across local filesystems
    </div>
  {/if}

  {#if fs.length > 0}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Filesystems</header>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-5 py-2.5">Mount</th>
              <th class="text-left font-medium px-3 py-2.5">FS</th>
              <th class="text-right font-medium px-3 py-2.5">Used</th>
              <th class="text-right font-medium px-3 py-2.5">Total</th>
              <th class="text-right font-medium px-5 py-2.5">Usage</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each fs as row (row.mount)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-5 py-2 text-zinc-100 font-mono text-xs">{row.mount}</td>
                <td class="px-3 py-2 text-zinc-400 font-mono text-xs">{row.fstype}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-300">{bytes(row.used)}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-400">{bytes(row.total)}</td>
                <td class="px-5 py-2 text-right">
                  <div class="flex items-center justify-end gap-2">
                    <div class="h-1.5 w-24 rounded-full bg-zinc-800 overflow-hidden">
                      <div class="h-full {row.pct > 90 ? 'bg-rose-400' : row.pct > 75 ? 'bg-amber-400' : 'bg-emerald-400'}" style="width: {Math.min(100, row.pct).toFixed(1)}%"></div>
                    </div>
                    <span class="numeric text-zinc-300 w-12 text-right">{pct(row.pct, 0)}</span>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}

  {#if raid.length > 0}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">RAID arrays</header>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-5 py-2.5">Array</th>
              <th class="text-left font-medium px-3 py-2.5">Type</th>
              <th class="text-left font-medium px-3 py-2.5">Status</th>
              <th class="text-right font-medium px-5 py-2.5">Rebuild</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each raid as row (row.array)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-5 py-2 text-zinc-100 font-mono text-xs">{row.array}</td>
                <td class="px-3 py-2 text-zinc-400 text-xs uppercase">{row.type ?? '—'}</td>
                <td class="px-3 py-2">
                  {#if row.degraded === 0}
                    <span class="inline-flex items-center rounded-md border border-emerald-900/60 bg-emerald-950/40 px-1.5 py-0.5 text-[10px] font-medium text-emerald-300">Optimal</span>
                  {:else}
                    <span class="inline-flex items-center rounded-md border border-rose-900/60 bg-rose-950/40 px-1.5 py-0.5 text-[10px] font-medium text-rose-300">Degraded</span>
                  {/if}
                </td>
                <td class="px-5 py-2 text-right">
                  {#if row.syncPct !== null}
                    <div class="flex items-center justify-end gap-2">
                      <div class="h-1.5 w-24 rounded-full bg-zinc-800 overflow-hidden">
                        <div class="h-full bg-amber-400" style="width: {Math.min(100, Math.max(0, row.syncPct)).toFixed(1)}%"></div>
                      </div>
                      <span class="numeric text-zinc-300 w-12 text-right">{pct(row.syncPct, 0)}</span>
                    </div>
                  {:else}
                    <span class="text-zinc-600 text-xs">—</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}

  {#if smart.length === 0 && smartEnabled && !loading && !error}
    {#if smartStatus?.state === 'no_devices'}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">SMART health</header>
        <div class="px-5 py-4 text-sm text-zinc-300 space-y-1">
          <p>No SMART-capable devices on this host.</p>
          <p class="text-zinc-500 text-xs">The agent ran <span class="font-mono text-zinc-400">smartctl --scan</span> successfully, but the system reported zero devices. This is normal for VPS instances, VMs, and storage behind hypervisors or USB enclosures that don't expose SMART.</p>
          <p class="text-zinc-500 text-xs">Drives behind a hardware RAID controller (MegaRAID/PERC, HP Smart Array, Adaptec, 3ware, Areca) are probed automatically; when a controller is detected but its drives stay hidden, a notice appears here instead.</p>
        </div>
      </section>
    {:else if smartStatus?.state === 'binary_missing'}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">SMART health</header>
        <div class="px-5 py-4 text-sm text-zinc-300 space-y-1">
          <p><span class="font-mono text-zinc-400">smartctl</span> is not installed on this host.</p>
          <p class="text-zinc-500 text-xs">Install <span class="font-mono text-zinc-400">smartmontools</span> (Linux: <span class="font-mono text-zinc-400">apt install smartmontools</span> or equivalent; Windows: smartmontools.org) and restart the agent.</p>
        </div>
      </section>
    {:else if smartStatus?.state === 'scan_failed'}
      <section class="rounded-xl border border-amber-900/50 bg-amber-950/20">
        <header class="px-5 py-3 border-b border-amber-900/40 text-xs uppercase tracking-wider text-amber-300/80">SMART health</header>
        <div class="px-5 py-4 text-sm text-amber-100/90 space-y-1">
          <p><span class="font-mono text-amber-200">smartctl --scan</span> failed on this host.</p>
          <p class="text-amber-100/70 text-xs">The most common cause is the agent not running with admin/root privileges. Re-install or run the service as a privileged user.</p>
          {#if smartStatus.message}
            <p class="text-amber-100/60 text-[11px] font-mono pt-1">{smartStatus.message}</p>
          {/if}
        </div>
      </section>
    {:else if smartStatus?.state === 'raid_unreadable'}
      <section class="rounded-xl border border-amber-900/50 bg-amber-950/20">
        <header class="px-5 py-3 border-b border-amber-900/40 text-xs uppercase tracking-wider text-amber-300/80">SMART health</header>
        <div class="px-5 py-4 text-sm text-amber-100/90 space-y-1">
          <p>A hardware RAID controller is hiding this host's drives.</p>
          <p class="text-amber-100/70 text-xs">The agent automatically uses Broadcom <span class="font-mono">storcli</span> or Dell <span class="font-mono">perccli</span> when installed at <span class="font-mono">/opt/MegaRAID/storcli/storcli64</span> or standard <span class="font-mono">sbin</span>/<span class="font-mono">bin</span> paths. It is picked up within about five minutes with no reconfiguration. You can still verify passthrough manually with <span class="font-mono">smartctl -d megaraid,N /dev/sdX</span> or check the controller with its vendor CLI.</p>
          <p class="text-amber-100/70 text-xs">If the detail below says the controller ioctl node is not accessible, the node was recreated root-owned at the last boot. <span class="font-mono">--enable-smart</span> installs a fixup that re-opens it to the <span class="font-mono">disk</span> group every time the agent starts; re-run the installer with that option on hosts installed before it existed.</p>
          {#if smartStatus.message}
            <p class="text-amber-100/60 text-[11px] font-mono pt-1">{smartStatus.message}</p>
          {/if}
        </div>
      </section>
    {:else if smartStatus?.state === 'read_failed'}
      <section class="rounded-xl border border-amber-900/50 bg-amber-950/20">
        <header class="px-5 py-3 border-b border-amber-900/40 text-xs uppercase tracking-wider text-amber-300/80">SMART health</header>
        <div class="px-5 py-4 text-sm text-amber-100/90 space-y-1">
          <p>Devices were detected, but the agent could not read SMART data from them.</p>
          <p class="text-amber-100/70 text-xs">On Linux this usually affects NVMe drives: <span class="font-mono">smartctl</span> reads them via <span class="font-mono">NVME_IOCTL_ADMIN_CMD</span>, which the kernel gates behind <span class="font-mono">CAP_SYS_ADMIN</span>. The agent's <span class="font-mono">CAP_SYS_RAWIO</span> covers SATA/SAS only. Re-run the installer with <span class="font-mono">--enable-smart-nvme</span> (<span class="font-mono">SM_ENABLE_SMART_NVME=1</span>) to additionally grant it. On Windows, install the agent as the Admin service.</p>
          {#if smartStatus.message}
            <p class="text-amber-100/60 text-[11px] font-mono pt-1">{smartStatus.message}</p>
          {/if}
        </div>
      </section>
    {:else}
      <section class="rounded-xl border border-amber-900/50 bg-amber-950/20">
        <header class="px-5 py-3 border-b border-amber-900/40 text-xs uppercase tracking-wider text-amber-300/80">SMART health</header>
        <div class="px-5 py-4 text-sm text-amber-100/90 space-y-2">
          <p>The <span class="font-mono text-amber-200">smart</span> collector is enabled, but no devices have reported SMART data yet.</p>
          <p class="text-amber-100/70 text-xs">If this persists for more than ten minutes, check that <span class="font-mono">smartctl</span> is installed and the agent has admin/root privileges.</p>
        </div>
      </section>
    {/if}
  {/if}

  {#if smart.length > 0}
    {#if smartStatus?.state === 'read_failed'}
      <div class="rounded-lg border border-amber-900/50 bg-amber-950/20 px-4 py-2.5 text-xs text-amber-100/80">
        Some devices were detected but could not be read{smartStatus.message ? ` — ${smartStatus.message}` : ''}. On Linux this usually means an NVMe drive that needs <span class="font-mono">CAP_SYS_ADMIN</span> (re-run the installer with <span class="font-mono">--enable-smart-nvme</span>), or drives behind a hardware RAID controller that refuses passthrough.
      </div>
    {:else if smartStatus?.state === 'raid_unreadable'}
      <div class="rounded-lg border border-amber-900/50 bg-amber-950/20 px-4 py-2.5 text-xs text-amber-100/80">
        A hardware RAID controller on this host is hiding some drives{smartStatus.message ? ` — ${smartStatus.message}` : ''}. The agent automatically uses installed Broadcom <span class="font-mono">storcli</span> or Dell <span class="font-mono">perccli</span>; the devices below are the ones it can still reach.
      </div>
    {/if}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between gap-3 px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">
        <span>SMART health</span>
        {#if smartFreshness}<span class="numeric normal-case tracking-normal {smartFreshness.stale ? 'text-amber-300' : 'text-zinc-500'}">{smartFreshness.text}</span>{/if}
      </header>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-5 py-2.5">Device</th>
              {#if smartHasSlots}<th class="text-left font-medium px-3 py-2.5">Slot</th>{/if}
              <th class="text-left font-medium px-3 py-2.5">Model</th>
              <th class="text-left font-medium px-3 py-2.5">Status</th>
              <th class="text-right font-medium px-3 py-2.5">Temp</th>
              <th class="text-right font-medium px-3 py-2.5">Power on</th>
              <th class="text-right font-medium px-3 py-2.5">Written</th>
              <th class="text-right font-medium px-3 py-2.5">Read</th>
              <th class="text-right font-medium px-3 py-2.5">Used</th>
              <th class="text-right font-medium px-3 py-2.5">Media err</th>
              <th class="text-right font-medium px-3 py-2.5">Unsafe</th>
              <th class="text-right font-medium px-3 py-2.5">Realloc</th>
              <th class="text-right font-medium px-5 py-2.5">Pending</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each smart as row (row.device)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-5 py-2 text-zinc-100 font-mono text-xs">{row.device}</td>
                {#if smartHasSlots}<td class="px-3 py-2 text-zinc-400 font-mono text-xs">{row.slot ?? '—'}</td>{/if}
                <td class="px-3 py-2 text-zinc-400 font-mono text-xs max-w-40 truncate" title={row.model ?? undefined}>{row.model ?? '—'}</td>
                <td class="px-3 py-2">
                  {#if row.healthy === 1}
                    <span class="inline-flex items-center rounded-md border border-emerald-900/60 bg-emerald-950/40 px-1.5 py-0.5 text-[10px] font-medium text-emerald-300">Passed</span>
                  {:else if row.healthy === 0}
                    <span class="inline-flex items-center rounded-md border border-rose-900/60 bg-rose-950/40 px-1.5 py-0.5 text-[10px] font-medium text-rose-300">Failed</span>
                  {:else}
                    <span class="text-zinc-600 text-xs">—</span>
                  {/if}
                </td>
                <td class="px-3 py-2 text-right numeric text-zinc-300">{row.tempC !== null ? `${row.tempC.toFixed(0)} °C` : '—'}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-400">{row.powerOnHours !== null ? dur(row.powerOnHours * 3600) : '—'}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-300">{row.written !== null ? bytes(row.written) : '—'}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-400">{row.read !== null ? bytes(row.read) : '—'}</td>
                <td class="px-3 py-2 text-right numeric {row.percentUsed !== null && row.percentUsed > 90 ? 'text-rose-300' : row.percentUsed !== null && row.percentUsed > 80 ? 'text-amber-300' : 'text-zinc-400'}">{row.percentUsed !== null ? pct(row.percentUsed, 0) : '—'}</td>
                <td class="px-3 py-2 text-right numeric {row.mediaErrors !== null && row.mediaErrors > 0 ? 'text-rose-300' : 'text-zinc-400'}">
                  {#if row.mediaErrors !== null && row.mediaErrors > 0}
                    <button type="button" onclick={() => openDetail(row, 'mediaErrors')}
                      class="underline decoration-dotted decoration-zinc-600 underline-offset-2 hover:decoration-rose-300"
                      title="See when this changed">{row.mediaErrors}</button>
                  {:else}{row.mediaErrors ?? '—'}{/if}
                </td>
                <td class="px-3 py-2 text-right numeric text-zinc-400">{row.unsafeShutdowns ?? '—'}</td>
                <td class="px-3 py-2 text-right numeric {row.realloc !== null && row.realloc > 0 ? 'text-amber-300' : 'text-zinc-400'}">
                  {#if row.realloc !== null && row.realloc > 0}
                    <button type="button" onclick={() => openDetail(row, 'realloc')}
                      class="underline decoration-dotted decoration-zinc-600 underline-offset-2 hover:decoration-amber-300"
                      title="See when this changed">{row.realloc}</button>
                  {:else}{row.realloc ?? '—'}{/if}
                </td>
                <td class="px-5 py-2 text-right numeric {row.pending !== null && row.pending > 0 ? 'text-rose-300' : 'text-zinc-400'}">
                  {#if row.pending !== null && row.pending > 0}
                    <button type="button" onclick={() => openDetail(row, 'pending')}
                      class="underline decoration-dotted decoration-zinc-600 underline-offset-2 hover:decoration-rose-300"
                      title="See when this changed">{row.pending}</button>
                  {:else}{row.pending ?? '—'}{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    {#if smartTemps.length > 0}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">SMART temperature</div>
          <DownloadCsv host={hostId} metric="smart_temp_c" splitBy="device" {range} />
        </header>
        <div class="px-3 py-3">
          <MultiChart series={toSeries(smartTemps)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="°C" format={(v, e = 0) => `${v.toFixed(e)} °C`} />
        </div>
      </section>
    {/if}

    {#if smartWritten.length > 0}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Data written</div>
          <DownloadCsv host={hostId} metric="smart_data_written_bytes" splitBy="device" {range} />
        </header>
        <div class="px-3 py-3">
          <MultiChart series={toSeries(smartWritten)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B" format={(v, e = 0) => bytes(v, 1 + e)} />
        </div>
      </section>
    {/if}

    {#if smartRead.length > 0}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Data read</div>
          <DownloadCsv host={hostId} metric="smart_data_read_bytes" splitBy="device" {range} />
        </header>
        <div class="px-3 py-3">
          <MultiChart series={toSeries(smartRead)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B" format={(v, e = 0) => bytes(v, 1 + e)} />
        </div>
      </section>
    {/if}
  {/if}

  {#if detailOpen}
    <div
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      use:modalFocus
      class="fixed inset-0 z-40 bg-zinc-950/60 backdrop-blur-sm flex items-center justify-center p-4"
      onclick={(e) => { if (e.target === e.currentTarget) closeDetail(); }}
      onkeydown={(e) => { if (e.key === 'Escape') closeDetail(); }}>
      <div class="w-full max-w-2xl rounded-xl border border-zinc-800 bg-zinc-900 shadow-2xl overflow-hidden">
        <header class="flex items-start justify-between gap-4 border-b border-zinc-800 px-5 py-4">
          <div class="min-w-0">
            <h2 class="truncate text-sm font-medium text-zinc-100">{smartCounterMeta[detailAttr]?.label} — {detailDevice}</h2>
            <p class="mt-1 truncate text-xs text-zinc-500">{detailModel}{detailSlot ? ' · slot ' + detailSlot : ''}</p>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <DownloadCsv host={hostId} metric={smartCounterMeta[detailAttr]?.metric ?? ''} labels={{ device: detailDevice }} range={detailWin} title="Download history CSV" />
            <button type="button" onclick={closeDetail} aria-label="Close" class="rounded-md p-1 text-zinc-400 hover:text-zinc-200">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="h-5 w-5" aria-hidden="true">
                <path d="M6 6l12 12M18 6L6 18" />
              </svg>
            </button>
          </div>
        </header>
        <div class="space-y-4 px-5 py-4">
          {#if detailError}
            <div class="rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{detailError}</div>
          {:else if !detailLoading}
            {#if detailAnalysis === null}
              <div class="rounded-md border border-zinc-800 bg-zinc-950/30 px-3 py-2 text-xs text-zinc-400">
                No history for this device in the last {MEDIA_HISTORY_DAYS} days.
              </div>
            {:else if detailAnalysis.firstVal >= detailAnalysis.current}
              {#if detailAnalysis.current > 0}
                <div class="rounded-md border border-amber-900/50 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
                  {detailAnalysis.current} recorded — unchanged across the last {MEDIA_HISTORY_DAYS} days (first observed {timeAgo(detailAnalysis.firstTs)}). The increase occurred before this window.
                </div>
              {:else}
                <div class="rounded-md border border-emerald-900/50 bg-emerald-950/30 px-3 py-2 text-xs text-emerald-300">No errors recorded.</div>
              {/if}
            {:else if detailAnalysis.firstVal === 0 && detailAnalysis.current > 0}
              <div class="rounded-md border border-emerald-900/50 bg-emerald-950/20 px-3 py-2 text-xs text-zinc-300">
                Rose from 0 to {detailAnalysis.current}. First non-zero {absDate(detailAnalysis.changes[0].ts)} ({timeAgo(detailAnalysis.changes[0].ts)}); last increase {timeAgo(detailAnalysis.changes.at(-1)?.ts)}.
              </div>
            {:else}
              <div class="rounded-md border border-amber-900/50 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
                Was {detailAnalysis.firstVal} at the start of this window ({timeAgo(detailAnalysis.firstTs)}) and rose to {detailAnalysis.current} — at least part predates the window.
              </div>
            {/if}
          {/if}

          <MultiChart
            series={[{ label: detailDevice, points: detailPoints }]}
            fromMs={detailWin.fromMs}
            toMs={detailWin.toMs}
            stepped
            loading={detailLoading}
            unit=""
            yClampMin={0}
            yMinSpan={4}
            format={(v, e = 0) => v.toFixed(e)} />

          {#if detailAnalysis && detailAnalysis.changes.length > 0}
            <div>
              <div class="mb-1.5 text-[10px] uppercase tracking-wider text-zinc-500">Observed changes in this window</div>
              <div class="max-h-40 overflow-y-auto rounded-md border border-zinc-800 divide-y divide-zinc-800/70">
                {#each [...detailAnalysis.changes].reverse() as c}
                  <div class="px-3 py-2 font-mono text-xs numeric text-zinc-300">{absDate(c.ts)} · {c.from} → {c.to}</div>
                {/each}
              </div>
              <p class="mt-1.5 text-[11px] text-zinc-600">Derived from stored samples; timing is accurate to the sampling interval.</p>
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</div>
