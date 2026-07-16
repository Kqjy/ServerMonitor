<script module lang="ts">
  export const chartPalette = [
    'oklch(0.78 0.16 162)',
    'oklch(0.7 0.18 240)',
    'oklch(0.83 0.18 85)',
    'oklch(0.7 0.21 22)',
    'oklch(0.75 0.18 305)',
    'oklch(0.78 0.16 195)',
    'oklch(0.75 0.18 50)',
    'oklch(0.77 0.18 130)'
  ];
</script>

<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import uPlot from 'uplot';
  import type { SeriesPoint } from '$lib/api';
  import { tooltipPlugin } from './uplotTooltip';
  import { tickDigits } from '$lib/format';

  export type Series = {
    label: string;
    color?: string;
    points: SeriesPoint[];
    stroke?: number;
  };

  export type ChartZoom = { fromMs: number; toMs: number } | null;

  let {
    series,
    unit = '',
    height = 220,
    fill = false,
    format,
    fromMs,
    toMs,
    emptyText = 'No data in this range',
    zoomed = false,
    loading = false,
    masking = false,
    yMinSpan,
    yClampMin,
    yMaxDigits,
    onZoom,
    onResetZoom
  }: {
    series: Series[];
    unit?: string;
    height?: number;
    fill?: boolean;
    format?: (v: number, extraDigits?: number) => string;
    fromMs?: number;
    toMs?: number;
    emptyText?: string;
    zoomed?: boolean;
    loading?: boolean;
    masking?: boolean;
    yMinSpan?: number;
    yClampMin?: number;
    yMaxDigits?: number;
    onZoom?: (fromMs: number, toMs: number) => void;
    onResetZoom?: () => void;
  } = $props();

  let host: HTMLDivElement;
  let plot: uPlot | null = null;
  let ro: ResizeObserver | null = null;
  let suppressBroadcast = false;
  let lastAppliedFromS = 0;
  let lastAppliedToS = 0;
  let lastHadData = false;
  let hidden = $state(new Set<string>());

  function currentFromS(): number {
    return (fromMs ?? 0) / 1000;
  }
  function currentToS(): number {
    return (toMs ?? 0) / 1000;
  }

  function alignData(s: Series[]): uPlot.AlignedData {
    const allTs = new Set<number>();
    for (const ser of s) {
      for (const p of ser.points) allTs.add(new Date(p.ts).getTime() / 1000);
    }
    const xs = Array.from(allTs).sort((a, b) => a - b);
    const result: number[][] = [xs];
    for (const ser of s) {
      const map = new Map<number, number>();
      for (const p of ser.points) {
        map.set(new Date(p.ts).getTime() / 1000, p.v);
      }
      result.push(xs.map((t) => (map.has(t) ? (map.get(t) as number) : NaN)));
    }
    return result as unknown as uPlot.AlignedData;
  }

  function near(a: number, b: number, durS: number): boolean {
    const tol = Math.max(durS * 0.001, 1);
    return Math.abs(a - b) < tol;
  }

  function hasAdjacentDuplicate(labels: string[]): boolean {
    for (let i = 1; i < labels.length; i++) {
      if (labels[i] === labels[i - 1]) return true;
    }
    return false;
  }

  function dedupeTickLabels(render: (extra: number) => string[]): string[] {
    let labels = render(0);
    for (let extra = 1; extra <= 4 && hasAdjacentDuplicate(labels); extra++) {
      labels = render(extra);
    }
    return labels;
  }

  function build(data: uPlot.AlignedData) {
    const w = Math.max(host.clientWidth, 200);
    const fromS = currentFromS();
    const toS = currentToS();
    lastAppliedFromS = fromS;
    lastAppliedToS = toS;
    const opts: uPlot.Options = {
      width: w,
      height,
      cursor: { drag: { x: true, y: false, setScale: true }, focus: { prox: 24 } },
      scales: {
        x: { time: true, auto: false },
        y: {
          auto: true,
          range: (_u, dMin, dMax) => {
            let lo = dMin;
            let hi = dMax;
            if (!Number.isFinite(lo) || !Number.isFinite(hi)) return [0, 1];
            if (lo === hi) {
              const bump = Math.max(Math.abs(lo) * 0.1, 1);
              lo -= bump;
              hi += bump;
            }
            if (yMinSpan != null && hi - lo < yMinSpan) {
              const mid = (lo + hi) / 2;
              lo = mid - yMinSpan / 2;
              hi = mid + yMinSpan / 2;
            }
            if (yClampMin != null && lo < yClampMin) {
              const shift = yClampMin - lo;
              lo += shift;
              hi += shift;
            }
            return [lo, hi];
          }
        }
      },
      hooks: {
        setScale: [
          (u, key) => {
            if (key !== 'x') return;
            const xMin = u.scales.x.min;
            const xMax = u.scales.x.max;
            if (xMin == null || xMax == null) return;
            const dur = lastAppliedToS - lastAppliedFromS;
            if (near(xMin, lastAppliedFromS, dur) && near(xMax, lastAppliedToS, dur)) return;
            lastAppliedFromS = xMin;
            lastAppliedToS = xMax;
            if (!suppressBroadcast && onZoom) {
              onZoom(xMin * 1000, xMax * 1000);
            }
          }
        ]
      },
      axes: [
        {
          scale: 'x',
          stroke: 'oklch(0.64 0 0)',
          grid: { stroke: 'oklch(0.27 0 0 / 0.6)', width: 0.5 },
          ticks: { stroke: 'oklch(0.27 0 0)', width: 0.5 },
          size: 28
        },
        {
          scale: 'y',
          stroke: 'oklch(0.64 0 0)',
          grid: { stroke: 'oklch(0.27 0 0 / 0.6)', width: 0.5 },
          ticks: { stroke: 'oklch(0.27 0 0)', width: 0.5 },
          values: (_u, ticks) => {
            if (format) return dedupeTickLabels((extra) => ticks.map((t) => format(t, extra)));
            let d = tickDigits(ticks);
            if (yMaxDigits != null && d > yMaxDigits) d = yMaxDigits;
            return dedupeTickLabels((extra) => ticks.map((t) => `${t.toFixed(d + extra)}${unit ? ' ' + unit : ''}`));
          },
          size: 64
        }
      ],
      series: [
        { label: 'time' },
        ...series.map((s, i) => {
          const c = s.color ?? chartPalette[i % chartPalette.length];
          return {
            label: s.label,
            show: !hidden.has(s.label),
            stroke: c,
            width: s.stroke ?? 1.5,
            spanGaps: true,
            fill: fill ? c.replace(')', ' / 0.08)') : undefined,
            points: { size: 5, fill: c, stroke: c, width: 0 }
          } as uPlot.Series;
        })
      ],
      legend: { show: false },
      plugins: [tooltipPlugin({ unit, format })]
    };
    plot = new uPlot(opts, data, host);
    plot.setScale('x', { min: fromS, max: toS });
  }

  function applyVisibility() {
    if (!plot) return;
    series.forEach((s, i) => {
      const sr = plot!.series[i + 1];
      if (!sr) return;
      const show = !hidden.has(s.label);
      if ((sr.show !== false) !== show) plot!.setSeries(i + 1, { show });
    });
  }

  function toggle(label: string) {
    const next = new Set(hidden);
    if (next.has(label)) next.delete(label);
    else next.add(label);
    hidden = next;
    applyVisibility();
  }

  function toggleAll() {
    hidden = hidden.size > 0 ? new Set<string>() : new Set(series.map((s) => s.label));
    applyVisibility();
  }

  function hasData(s: Series[]) {
    for (const ser of s) if (ser.points.length > 0) return true;
    return false;
  }

  const empty = $derived(!hasData(series));
  const showLoading = $derived(loading || masking);
  const showEmpty = $derived(!loading && !masking && empty);
  const maskStale = $derived(masking && !empty);

  $effect(() => {
    const targetFromS = currentFromS();
    const targetToS = currentToS();
    if (!(targetToS > targetFromS)) return;
    if (plot) {
      const desiredW = Math.max(host.clientWidth, 200);
      if (plot.width !== desiredW || plot.height !== height) {
        plot.setSize({ width: desiredW, height });
      }
      const nowHasData = hasData(series);
      const data = nowHasData
        ? alignData(series)
        : ([[], ...series.map(() => [])] as unknown as uPlot.AlignedData);
      const dataRefilled = nowHasData && !lastHadData;
      plot.setData(data, dataRefilled);
      lastHadData = nowHasData;
      if (targetFromS !== lastAppliedFromS || targetToS !== lastAppliedToS) {
        lastAppliedFromS = targetFromS;
        lastAppliedToS = targetToS;
        suppressBroadcast = true;
        plot.setScale('x', { min: targetFromS, max: targetToS });
        queueMicrotask(() => { suppressBroadcast = false; });
      } else if (!dataRefilled) {
        plot.redraw();
      }
    } else if (hasData(series)) {
      build(alignData(series));
      lastHadData = true;
    }
  });

  onMount(() => {
    ro = new ResizeObserver(() => {
      if (plot && host) {
        const w = Math.max(host.clientWidth, 200);
        if (plot.width !== w) plot.setSize({ width: w, height });
      }
    });
    ro.observe(host);
  });

  onDestroy(() => {
    ro?.disconnect();
    plot?.destroy();
  });
</script>

<div class="relative">
  <div bind:this={host} class="w-full" style="height: {height}px"></div>
  {#if showLoading}
    <div class="absolute inset-0 px-3 pointer-events-none" class:bg-zinc-950={maskStale} class:rounded-md={maskStale} aria-busy="true" aria-label="Loading chart">
      <div class="h-full w-full rounded-md shimmer opacity-40"></div>
      <div class="absolute inset-0 flex items-center justify-center">
        <div class="flex items-center gap-2 px-3 py-1.5 rounded-md border border-zinc-800 bg-zinc-950/80 text-[11px] uppercase tracking-wider text-zinc-400">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5 animate-spin" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M21 12a9 9 0 1 1-6.219-8.56" />
          </svg>
          Loading
        </div>
      </div>
    </div>
  {/if}
  {#if showEmpty}
    <div class="absolute inset-0 flex items-center justify-center pointer-events-none">
      <div class="text-[11px] uppercase tracking-wider text-zinc-500 px-3 py-1.5 rounded-md border border-zinc-800 bg-zinc-950/80">
        {emptyText}
      </div>
    </div>
  {/if}
  {#if zoomed}
    <button
      type="button"
      onclick={() => onResetZoom?.()}
      class="absolute top-2 right-2 inline-flex items-center gap-1.5 px-2 py-1 rounded-md text-[10px] uppercase tracking-wider text-zinc-300 border border-zinc-700 bg-zinc-900/80 hover:bg-zinc-800 transition-colors">
      <svg viewBox="0 0 24 24" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M3 12a9 9 0 1 0 3-6.7" />
        <path d="M3 4v5h5" />
      </svg>
      Reset zoom
    </button>
  {/if}
</div>

{#if series.length > 1}
  <div class="mt-2 flex items-start justify-between gap-3">
    <div class="flex flex-wrap gap-x-3 gap-y-1 text-[11px] numeric">
      {#each series as s, i (s.label)}
        {@const off = hidden.has(s.label)}
        <button
          type="button"
          onclick={() => toggle(s.label)}
          aria-pressed={!off}
          title={off ? 'Show series' : 'Hide series'}
          class="inline-flex items-center gap-1.5 transition-opacity {off ? 'opacity-40 hover:opacity-70' : 'text-zinc-400 hover:text-zinc-200'}">
          <span class="inline-block h-1.5 w-3 rounded-sm" style="background: {s.color ?? chartPalette[i % chartPalette.length]}"></span>
          <span class:line-through={off}>{s.label}</span>
        </button>
      {/each}
    </div>
    {#if series.length > 2}
      <button
        type="button"
        onclick={toggleAll}
        class="shrink-0 text-[10px] uppercase tracking-wider text-zinc-500 hover:text-zinc-300 px-1.5 py-0.5 rounded border border-zinc-800 hover:border-zinc-700 transition-colors">
        {hidden.size > 0 ? 'Show all' : 'Hide all'}
      </button>
    {/if}
  </div>
{/if}
