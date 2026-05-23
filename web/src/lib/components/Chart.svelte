<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import uPlot from 'uplot';
  import type { SeriesPoint } from '$lib/api';
  import { tooltipPlugin, type TooltipFormat } from './uplotTooltip';
  import { tickDigits } from '$lib/format';

  let { points, unit = '', label = 'value', height = 220, color = 'oklch(0.78 0.16 162)', format }: {
    points: SeriesPoint[];
    unit?: string;
    label?: string;
    height?: number;
    color?: string;
    format?: TooltipFormat;
  } = $props();

  let host: HTMLDivElement;
  let plot: uPlot | null = null;
  let ro: ResizeObserver | null = null;

  function build(data: uPlot.AlignedData) {
    const opts: uPlot.Options = {
      width: host.clientWidth,
      height,
      cursor: { drag: { x: true, y: false }, focus: { prox: 24 } },
      scales: { x: { time: true }, y: { auto: true } },
      axes: [
        {
          stroke: 'oklch(0.64 0 0)',
          grid: { stroke: 'oklch(0.27 0 0)', width: 0.5 },
          ticks: { stroke: 'oklch(0.27 0 0)', width: 0.5 }
        },
        {
          stroke: 'oklch(0.64 0 0)',
          grid: { stroke: 'oklch(0.27 0 0)', width: 0.5 },
          ticks: { stroke: 'oklch(0.27 0 0)', width: 0.5 },
          values: (_u, ticks) => {
            const d = tickDigits(ticks);
            return ticks.map((t) => `${t.toFixed(d)}${unit ? ' ' + unit : ''}`);
          },
          size: 56
        }
      ],
      series: [
        {},
        {
          label,
          stroke: color,
          width: 1.5,
          fill: color.replace(')', ' / 0.08)').replace('oklch(', 'oklch('),
          points: { show: false }
        }
      ],
      legend: { show: false },
      padding: [8, 16, 0, 0],
      plugins: [tooltipPlugin({ unit, format })]
    };
    plot = new uPlot(opts, data, host);
  }

  function toAligned(p: SeriesPoint[]): uPlot.AlignedData {
    const xs = new Float64Array(p.length);
    const ys = new Float64Array(p.length);
    for (let i = 0; i < p.length; i++) {
      xs[i] = new Date(p[i].ts).getTime() / 1000;
      ys[i] = p[i].v;
    }
    return [xs as unknown as number[], ys as unknown as number[]];
  }

  $effect(() => {
    if (!plot) return;
    plot.setData(toAligned(points));
  });

  onMount(() => {
    build(toAligned(points));
    ro = new ResizeObserver(() => {
      if (plot && host) plot.setSize({ width: host.clientWidth, height });
    });
    ro.observe(host);
  });

  onDestroy(() => {
    ro?.disconnect();
    plot?.destroy();
  });
</script>

<div bind:this={host} class="w-full" style="height: {height}px"></div>
