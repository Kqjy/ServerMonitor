<script lang="ts">
  export type BarEvent = {
    repo: string;
    timeMs: number;
    value: number | null;
  };

  let {
    events,
    format,
    repoColor,
    height = 220,
    emptyText = 'No snapshots yet',
    tickUnit = 'bytes'
  }: {
    events: BarEvent[];
    format: (v: number, extraDigits?: number) => string;
    repoColor: (repo: string) => string;
    height?: number;
    emptyText?: string;
    tickUnit?: 'bytes' | 'seconds';
  } = $props();

  let width = $state(0);
  let hovered = $state<number | null>(null);
  let tooltipWidth = $state(190);

  const leftGutter = 56;
  const bottomGutter = 30;
  const topPadding = 8;
  const byteTickSteps = Array.from({ length: 5 }, (_, power) =>
    [1, 2, 5, 10, 20, 50, 100, 200, 500].map((value) => value * 1024 ** power)
  ).flat();
  const secondTickSteps = [
    1, 2, 5, 10, 15, 30,
    60, 120, 300, 600, 900, 1800,
    3600, 7200, 10800, 21600, 43200,
    86400, 172800, 604800
  ];
  const plotWidth = $derived(Math.max(0, width - leftGutter));
  const baseline = $derived(Math.max(topPadding, height - bottomGutter));
  const plotHeight = $derived(Math.max(0, baseline - topPadding));
  const slotWidth = $derived(events.length > 0 ? plotWidth / events.length : 0);
  const barWidth = $derived(Math.min(28, Math.max(4, slotWidth * 0.6)));
  const reportedValues = $derived(events.flatMap((event) => event.value === null || !Number.isFinite(event.value) ? [] : [Math.max(0, event.value)]));
  const hasReportedValues = $derived(reportedValues.length > 0);
  const maxValue = $derived(hasReportedValues ? Math.max(...reportedValues) : 0);
  const chartScale = $derived.by(() => {
    const step = (tickUnit === 'seconds' ? secondTickSteps : byteTickSteps).find((value) => 3 * value >= maxValue * 1.05);
    if (step !== undefined) return { max: 3 * step, ticks: [0, step, 2 * step, 3 * step] };
    const max = maxValue > 0 ? maxValue * 1.08 : 1;
    return { max, ticks: [0, max / 3, max * 2 / 3, max] };
  });
  const scaleMax = $derived(chartScale.max);
  const ticks = $derived(chartScale.ticks);
  const tickLabels = $derived.by(() => {
    let labels = ticks.map((tick) => format(tick, 0));
    for (let extra = 1; extra <= 4 && labels.some((label, index) => index > 0 && label === labels[index - 1]); extra++) {
      labels = ticks.map((tick) => format(tick, extra));
    }
    return labels;
  });
  const labelCapacity = $derived(Math.max(1, Math.floor(plotWidth / 88)));
  const labelStep = $derived(Math.max(1, Math.ceil(events.length / labelCapacity)));
  const oneCalendarDay = $derived.by(() => {
    if (events.length === 0) return false;
    const first = new Date(events[0].timeMs);
    return events.every((event) => {
      const current = new Date(event.timeMs);
      return current.getFullYear() === first.getFullYear() && current.getMonth() === first.getMonth() && current.getDate() === first.getDate();
    });
  });
  const hoveredEvent = $derived(hovered === null ? null : events[hovered] ?? null);
  const tooltipLeft = $derived.by(() => {
    if (hovered === null) return 0;
    const centered = leftGutter + (hovered + 0.5) * slotWidth - tooltipWidth / 2;
    return Math.max(4, Math.min(Math.max(4, width - tooltipWidth - 4), centered));
  });
  const tooltipTop = $derived.by(() => {
    if (!hoveredEvent || hoveredEvent.value === null) return topPadding + 4;
    const value = Math.max(0, hoveredEvent.value);
    const barHeight = value === 0 ? 2 : Math.max(2, value / scaleMax * plotHeight);
    return Math.max(4, Math.min(height - 66, baseline - barHeight - 60));
  });

  function barHeight(value: number): number {
    if (value === 0) return 2;
    return Math.max(2, Math.max(0, value) / scaleMax * plotHeight);
  }

  function barOpacity(index: number, value: number): number {
    const valueOpacity = value === 0 ? 0.35 : 1;
    return hovered !== null && hovered !== index ? valueOpacity * 0.45 : valueOpacity;
  }

  function tickY(value: number): number {
    return baseline - value / scaleMax * plotHeight;
  }

  function xLabel(timeMs: number): string {
    const date = new Date(timeMs);
    return date.toLocaleString(undefined, oneCalendarDay
      ? { hour: '2-digit', minute: '2-digit', hour12: false }
      : { day: 'numeric', month: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
  }
</script>

<div class="relative w-full" bind:clientWidth={width} style="height: {height}px">
  {#if events.length > 0 && hasReportedValues}
    <svg class="absolute inset-0" {width} {height} role="img" aria-label={`Backup snapshot bar chart with ${events.length} events`}>
      {#each ticks as tick, index (index)}
        {@const y = tickY(tick)}
        <line x1={leftGutter} x2={width} y1={y} y2={y} stroke="oklch(0.27 0 0 / 0.6)" stroke-width="0.5" />
        <text x={leftGutter - 7} y={y + 3} text-anchor="end" class="text-[10px] numeric" fill="oklch(0.64 0 0)">{tickLabels[index]}</text>
      {/each}
      {#each events as event, index (`${event.repo}-${event.timeMs}-${index}`)}
        {@const centerX = leftGutter + (index + 0.5) * slotWidth}
        {#if event.value !== null && Number.isFinite(event.value)}
          {@const renderedHeight = barHeight(Math.max(0, event.value))}
          <rect
            x={centerX - barWidth / 2}
            y={baseline - renderedHeight}
            width={barWidth}
            height={renderedHeight}
            rx="1.5"
            fill={repoColor(event.repo)}
            opacity={barOpacity(index, event.value)} />
        {/if}
        {#if index % labelStep === 0}
          <text x={centerX} y={baseline + 18} text-anchor="middle" class="text-[10px] numeric" fill="oklch(0.64 0 0)">{xLabel(event.timeMs)}</text>
        {/if}
        <rect
          x={leftGutter + index * slotWidth}
          y="0"
          width={slotWidth}
          height={height}
          fill="transparent"
          role="img"
          aria-label={`${event.repo}, ${new Date(event.timeMs).toLocaleString()}, ${event.value === null ? 'not reported' : format(event.value)}`}
          onmouseenter={() => (hovered = index)}
          onmouseleave={() => (hovered = null)} />
      {/each}
    </svg>
    {#if hoveredEvent}
      <div
        bind:clientWidth={tooltipWidth}
        class="pointer-events-none absolute rounded-md border border-zinc-700 bg-zinc-950/95 px-2.5 py-1.5 text-xs shadow-lg"
        style="left: {tooltipLeft}px; top: {tooltipTop}px">
        <div class="flex items-center gap-1.5 font-mono text-zinc-200">
          <span class="h-1.5 w-1.5 rounded-full" style="background: {repoColor(hoveredEvent.repo)}"></span>
          <span>{hoveredEvent.repo}</span>
        </div>
        <div class="mt-0.5 text-zinc-400 numeric">{new Date(hoveredEvent.timeMs).toLocaleString()}</div>
        {#if hoveredEvent.value === null}
          <div class="mt-0.5 text-zinc-500">not reported</div>
        {:else}
          <div class="mt-0.5 text-zinc-100 numeric">{format(hoveredEvent.value)}</div>
        {/if}
      </div>
    {/if}
  {:else}
    <div class="absolute inset-0 flex items-center justify-center pointer-events-none">
      <div class="text-[11px] uppercase tracking-wider text-zinc-500 px-3 py-1.5 rounded-md border border-zinc-800 bg-zinc-950/80">
        {emptyText}
      </div>
    </div>
  {/if}
</div>
