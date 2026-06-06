<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry, type CollectorStatus } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, type Range } from '$lib/time';
  import { bytes, pct, dur } from '$lib/format';
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

  let read = $state<SeriesEntry[]>([]);
  let write = $state<SeriesEntry[]>([]);
  let readOps = $state<SeriesEntry[]>([]);
  let writeOps = $state<SeriesEntry[]>([]);
  let fs = $state<{ mount: string; fstype: string; used: number; total: number; pct: number }[]>([]);
  let fsUsedPct = $state<SeriesEntry[]>([]);
  let smartTemps = $state<SeriesEntry[]>([]);
  let smartWritten = $state<SeriesEntry[]>([]);
  let smartRead = $state<SeriesEntry[]>([]);
  let smart = $state<SmartRow[]>([]);
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
      label: e.labels.device ?? e.labels.mount ?? Object.values(e.labels).join(' '),
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
      const [r, wr, rOps, wOps, fsTotal, fsUsed, fsPct, sTemp, sHealthy, sHours, sRealloc, sPending, sWritten, sRead, sUsed, sMedia, sUnsafe, fsUsage] = await Promise.all([
        api.seriesMulti({ host: hostId, metric: 'disk_read_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_write_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_read_ops', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'disk_write_ops', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_total', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'fs_used_pct', from: '-2m', step: 30, splitBy: 'mount', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_temp_c', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_healthy', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_power_on_hours', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_realloc_sectors', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_pending_sectors', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_data_written_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_data_read_bytes', from, to, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_percent_used', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_media_errors', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'smart_unsafe_shutdowns', from: '-5m', step: 30, splitBy: 'device', signal: ac.signal }),
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

      const byDev: Record<string, SmartRow> = {};
      const ensureDev = (d: string): SmartRow => {
        if (!byDev[d]) byDev[d] = { device: d, healthy: null, powerOnHours: null, realloc: null, pending: null, tempC: null, written: null, read: null, percentUsed: null, mediaErrors: null, unsafeShutdowns: null };
        return byDev[d];
      };
      for (const e of sHealthy.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).healthy = e.points.at(-1)?.v ?? null;
      }
      for (const e of sHours.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).powerOnHours = e.points.at(-1)?.v ?? null;
      }
      for (const e of sRealloc.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).realloc = e.points.at(-1)?.v ?? null;
      }
      for (const e of sPending.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).pending = e.points.at(-1)?.v ?? null;
      }
      for (const e of sTemp.series) {
        const d = e.labels.device;
        if (!d) continue;
        const v = e.points.at(-1)?.v;
        if (v !== undefined) ensureDev(d).tempC = v;
      }
      for (const e of sWritten.series) {
        const d = e.labels.device;
        if (!d) continue;
        const v = e.points.at(-1)?.v;
        if (v !== undefined) ensureDev(d).written = v;
      }
      for (const e of sRead.series) {
        const d = e.labels.device;
        if (!d) continue;
        const v = e.points.at(-1)?.v;
        if (v !== undefined) ensureDev(d).read = v;
      }
      for (const e of sUsed.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).percentUsed = e.points.at(-1)?.v ?? null;
      }
      for (const e of sMedia.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).mediaErrors = e.points.at(-1)?.v ?? null;
      }
      for (const e of sUnsafe.series) {
        const d = e.labels.device;
        if (!d) continue;
        ensureDev(d).unsafeShutdowns = e.points.at(-1)?.v ?? null;
      }
      smart = Object.values(byDev).sort((a, b) => a.device.localeCompare(b.device));
      smartTemps = sTemp.series;
      smartWritten = sWritten.series;
      smartRead = sRead.series;

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

<div class="space-y-6">
  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Read throughput</div>
      <DownloadCsv host={hostId} metric="disk_read_bytes" splitBy="device" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(read)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v) => `${bytes(v, 0)}/s`} />
    </div>
  </section>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Write throughput</div>
      <DownloadCsv host={hostId} metric="disk_write_bytes" splitBy="device" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(write)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v) => `${bytes(v, 0)}/s`} />
    </div>
  </section>

  <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Read IOPS</div>
        <DownloadCsv host={hostId} metric="disk_read_ops" splitBy="device" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(readOps)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="ops/s" format={(v) => `${v.toFixed(0)}/s`} />
      </div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Write IOPS</div>
        <DownloadCsv host={hostId} metric="disk_write_ops" splitBy="device" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(writeOps)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="ops/s" format={(v) => `${v.toFixed(0)}/s`} />
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
        <MultiChart series={toSeries(fsUsedPct)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="%" format={(v) => pct(v, 1)} />
      </div>
    </section>
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

  {#if smart.length === 0 && smartEnabled && !loading}
    {#if smartStatus?.state === 'no_devices'}
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">SMART health</header>
        <div class="px-5 py-4 text-sm text-zinc-300 space-y-1">
          <p>No SMART-capable devices on this host.</p>
          <p class="text-zinc-500 text-xs">The agent ran <span class="font-mono text-zinc-400">smartctl --scan</span> successfully, but the system reported zero devices. This is normal for VPS instances, VMs, and storage behind hypervisors or USB enclosures that don't expose SMART.</p>
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
          <p class="text-amber-100/70 text-xs">If this persists for more than a minute, check that <span class="font-mono">smartctl</span> is installed and the agent has admin/root privileges.</p>
        </div>
      </section>
    {/if}
  {/if}

  {#if smart.length > 0}
    {#if smartStatus?.state === 'read_failed'}
      <div class="rounded-lg border border-amber-900/50 bg-amber-950/20 px-4 py-2.5 text-xs text-amber-100/80">
        Some devices were detected but could not be read{smartStatus.message ? ` — ${smartStatus.message}` : ''}. On Linux this usually means an NVMe drive that needs <span class="font-mono">CAP_SYS_ADMIN</span> (re-run the installer with <span class="font-mono">--enable-smart-nvme</span>).
      </div>
    {/if}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">SMART health</header>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-5 py-2.5">Device</th>
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
                <td class="px-3 py-2 text-right numeric {row.mediaErrors !== null && row.mediaErrors > 0 ? 'text-rose-300' : 'text-zinc-400'}">{row.mediaErrors ?? '—'}</td>
                <td class="px-3 py-2 text-right numeric text-zinc-400">{row.unsafeShutdowns ?? '—'}</td>
                <td class="px-3 py-2 text-right numeric {row.realloc !== null && row.realloc > 0 ? 'text-amber-300' : 'text-zinc-400'}">{row.realloc ?? '—'}</td>
                <td class="px-5 py-2 text-right numeric {row.pending !== null && row.pending > 0 ? 'text-rose-300' : 'text-zinc-400'}">{row.pending ?? '—'}</td>
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
          <MultiChart series={toSeries(smartTemps)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="°C" format={(v) => `${v.toFixed(0)} °C`} />
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
          <MultiChart series={toSeries(smartWritten)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B" format={(v) => bytes(v)} />
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
          <MultiChart series={toSeries(smartRead)} {fromMs} {toMs} zoomed={isZoomed} {masking} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B" format={(v) => bytes(v)} />
        </div>
      </section>
    {/if}
  {/if}
</div>
