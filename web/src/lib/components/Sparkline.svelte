<script lang="ts">
  import type { SeriesPoint } from '$lib/api';

  let { points, color = 'oklch(0.78 0.16 162)', height = 40, yMin, yMax, fill = false }: {
    points: SeriesPoint[];
    color?: string;
    height?: number;
    yMin?: number;
    yMax?: number;
    fill?: boolean;
  } = $props();

  let width = $state(160);
  let host: HTMLDivElement;

  $effect(() => {
    if (!host) return;
    const ro = new ResizeObserver(() => {
      width = host.clientWidth || 160;
    });
    ro.observe(host);
    return () => ro.disconnect();
  });

  const geometry = $derived.by(() => {
    if (!points.length) return { line: '', area: '' };
    const xs = points.map((p) => new Date(p.ts).getTime());
    const ys = points.map((p) => p.v);
    const xmin = xs[0];
    const xmax = xs[xs.length - 1];
    const xspan = Math.max(1, xmax - xmin);
    const lo = yMin ?? Math.min(...ys);
    const hi = yMax ?? Math.max(...ys);
    const yspan = Math.max(0.0001, hi - lo);
    const w = width;
    const h = height;
    const coords = points.map((p, i) => {
      const x = ((xs[i] - xmin) / xspan) * w;
      const yClamped = Math.max(lo, Math.min(hi, ys[i]));
      const y = h - ((yClamped - lo) / yspan) * (h - 4) - 2;
      return { x, y };
    });
    const line = coords.map((c, i) => `${i === 0 ? 'M' : 'L'}${c.x.toFixed(1)} ${c.y.toFixed(1)}`).join(' ');
    const area = fill && coords.length
      ? `${line} L${coords[coords.length - 1].x.toFixed(1)} ${h} L${coords[0].x.toFixed(1)} ${h} Z`
      : '';
    return { line, area };
  });
</script>

<div bind:this={host} class="w-full" style="height: {height}px">
  {#if points.length > 1}
    <svg viewBox={`0 0 ${width} ${height}`} class="w-full h-full" preserveAspectRatio="none">
      {#if geometry.area}
        <path d={geometry.area} fill={color} fill-opacity="0.15" stroke="none" />
      {/if}
      <path d={geometry.line} fill="none" stroke={color} stroke-width="1.5" stroke-linejoin="round" />
    </svg>
  {:else}
    <svg viewBox={`0 0 ${width} ${height}`} class="w-full h-full text-zinc-700" preserveAspectRatio="none">
      <line x1="0" y1={height / 2} x2={width} y2={height / 2} stroke="currentColor" stroke-width="1" stroke-dasharray="3 3" />
    </svg>
  {/if}
</div>
