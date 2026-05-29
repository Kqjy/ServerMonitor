<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type SeriesEntry } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, type Range } from '$lib/time';
  import { bytes } from '$lib/format';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';

  let { hostId, range }: { hostId: number; range: Range } = $props();

  let rx = $state<SeriesEntry[]>([]);
  let tx = $state<SeriesEntry[]>([]);
  let rxPkts = $state<SeriesEntry[]>([]);
  let txPkts = $state<SeriesEntry[]>([]);
  let rxErr = $state<SeriesEntry[]>([]);
  let txErr = $state<SeriesEntry[]>([]);
  let rxDrop = $state<SeriesEntry[]>([]);
  let txDrop = $state<SeriesEntry[]>([]);
  let estab = $state(0);
  let listen = $state(0);
  let timeWait = $state(0);
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
      label: e.labels.iface ?? Object.values(e.labels).join(' '),
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
      const [r, t, rp, tp, re, te, rd, td, e, l, w] = await Promise.all([
        api.seriesMulti({ host: hostId, metric: 'net_rx_bytes', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_tx_bytes', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_rx_packets', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_tx_packets', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_rx_errors', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_tx_errors', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_rx_dropped', from, to, splitBy: 'iface', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'net_tx_dropped', from, to, splitBy: 'iface', signal: ac.signal }),
        api.series({ host: hostId, metric: 'conn_estab', from: '-2m', step: 10, signal: ac.signal }),
        api.series({ host: hostId, metric: 'conn_listen', from: '-2m', step: 10, signal: ac.signal }),
        api.series({ host: hostId, metric: 'conn_timewait', from: '-2m', step: 10, signal: ac.signal })
      ]);
      if (gen !== refreshGen) return;
      rx = r.series;
      tx = t.series;
      rxPkts = rp.series;
      txPkts = tp.series;
      rxErr = re.series;
      txErr = te.series;
      rxDrop = rd.series;
      txDrop = td.series;
      estab = e.points.at(-1)?.v ?? 0;
      listen = l.points.at(-1)?.v ?? 0;
      timeWait = w.points.at(-1)?.v ?? 0;
      loading = false;
    } catch (err) {
      if (gen !== refreshGen) return;
      if ((err as { name?: string })?.name === 'AbortError') return;
      loading = false;
    }
  }

  $effect(() => {
    const current = range;
    if (prevRange !== null && !rangeEquals(current, prevRange)) {
      chartZoom = null;
      loading = true;
      rx = [];
      tx = [];
      rxPkts = [];
      txPkts = [];
      rxErr = [];
      txErr = [];
      rxDrop = [];
      txDrop = [];
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

  function lastSum(entries: SeriesEntry[]): number {
    let s = 0;
    for (const e of entries) s += e.points.at(-1)?.v ?? 0;
    return s;
  }
  const errorsTotal = $derived(lastSum(rxErr) + lastSum(txErr));
  const dropsTotal = $derived(lastSum(rxDrop) + lastSum(txDrop));
  const errorsTone = $derived(errorsTotal > 10 ? 'bad' : errorsTotal > 0 ? 'warn' : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const dropsTone = $derived(dropsTotal > 10 ? 'bad' : dropsTotal > 0 ? 'warn' : 'neutral') as 'good' | 'warn' | 'bad' | 'neutral';
  const hasErrorTraffic = $derived(rxErr.length + txErr.length + rxDrop.length + txDrop.length > 0);

  function sumSeries(entries: SeriesEntry[], label: string, color: string): Series | null {
    const acc = new Map<string, number>();
    for (const e of entries) {
      for (const p of e.points) acc.set(p.ts, (acc.get(p.ts) ?? 0) + p.v);
    }
    if (acc.size === 0) return null;
    const points = Array.from(acc.entries())
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([ts, v]) => ({ ts, v }));
    return { label, color, points };
  }

  const errorDropSeries = $derived((() => {
    const out: Series[] = [];
    const a = sumSeries(rxErr, 'rx err', 'oklch(0.7 0.21 22)');
    const b = sumSeries(txErr, 'tx err', 'oklch(0.83 0.18 85)');
    const c = sumSeries(rxDrop, 'rx drop', 'oklch(0.75 0.18 305)');
    const d = sumSeries(txDrop, 'tx drop', 'oklch(0.78 0.16 195)');
    for (const s of [a, b, c, d]) if (s) out.push(s);
    return out;
  })());
</script>

<div class="space-y-6">
  <div class="grid grid-cols-2 md:grid-cols-5 gap-3">
    <StatCard label="Established" value={estab.toFixed(0)} {loading} />
    <StatCard label="Listening" value={listen.toFixed(0)} {loading} />
    <StatCard label="Time wait" value={timeWait.toFixed(0)} {loading} />
    <StatCard label="Errors/s" value={errorsTotal.toFixed(2)} tone={errorsTone} {loading} />
    <StatCard label="Drops/s" value={dropsTotal.toFixed(2)} tone={dropsTone} {loading} />
  </div>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Receive</div>
      <DownloadCsv host={hostId} metric="net_rx_bytes" splitBy="iface" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(rx)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v) => `${bytes(v, 0)}/s`} />
    </div>
  </section>

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
      <div class="text-xs uppercase tracking-wider text-zinc-500">Transmit</div>
      <DownloadCsv host={hostId} metric="net_tx_bytes" splitBy="iface" {range} />
    </header>
    <div class="px-3 py-3">
      <MultiChart series={toSeries(tx)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="B/s" format={(v) => `${bytes(v, 0)}/s`} />
    </div>
  </section>

  <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Receive packets</div>
        <DownloadCsv host={hostId} metric="net_rx_packets" splitBy="iface" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(rxPkts)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="pps" format={(v) => `${v.toFixed(0)}/s`} />
      </div>
    </section>
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Transmit packets</div>
        <DownloadCsv host={hostId} metric="net_tx_packets" splitBy="iface" {range} />
      </header>
      <div class="px-3 py-3">
        <MultiChart series={toSeries(txPkts)} {fromMs} {toMs} zoomed={isZoomed} {loading} onZoom={handleZoom} onResetZoom={handleReset} unit="pps" format={(v) => `${v.toFixed(0)}/s`} />
      </div>
    </section>
  </div>

  {#if hasErrorTraffic && (errorsTotal > 0 || dropsTotal > 0)}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="px-5 py-3 border-b border-zinc-800 text-xs uppercase tracking-wider text-zinc-500">Errors & drops</header>
      <div class="px-3 py-3">
        <MultiChart
          series={errorDropSeries}
          {fromMs}
          {toMs}
          zoomed={isZoomed}
          {loading}
          onZoom={handleZoom}
          onResetZoom={handleReset}
          unit="/s"
          format={(v) => `${v.toFixed(2)}/s`} />
      </div>
    </section>
  {/if}
</div>
