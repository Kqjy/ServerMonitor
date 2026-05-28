<script lang="ts">
  import { rangeToFrom, rangeToTo, type Range } from '$lib/time';

  let {
    host,
    metric,
    range,
    splitBy,
    labels,
    step,
    title
  }: {
    host: number;
    metric: string;
    range: Range;
    splitBy?: string;
    labels?: Record<string, string>;
    step?: number;
    title?: string;
  } = $props();

  const href = $derived.by(() => {
    const path = splitBy ? '/api/v1/series/multi' : '/api/v1/series';
    const q = new URLSearchParams();
    q.set('host', String(host));
    q.set('metric', metric);
    const from = rangeToFrom(range);
    const to = rangeToTo(range);
    if (from) q.set('from', from);
    if (to) q.set('to', to);
    if (step) q.set('step', String(step));
    if (splitBy) q.set('split_by', splitBy);
    if (labels) q.set('labels', JSON.stringify(labels));
    q.set('format', 'csv');
    return `${path}?${q}`;
  });
</script>

<a
  {href}
  download
  title={title ?? `Download ${metric} as CSV`}
  aria-label="Download CSV"
  class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-zinc-500 hover:text-zinc-200 hover:bg-zinc-800/60 transition-colors text-[10px] uppercase tracking-wider"
>
  <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
    <path d="M12 3v12" />
    <path d="m7 10 5 5 5-5" />
    <path d="M5 21h14" />
  </svg>
  <span>CSV</span>
</a>
