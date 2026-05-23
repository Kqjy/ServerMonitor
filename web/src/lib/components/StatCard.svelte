<script lang="ts">
  let {
    label,
    value,
    sub,
    tone = 'neutral',
    loading = false
  }: {
    label: string;
    value: string;
    sub?: string;
    tone?: 'neutral' | 'good' | 'warn' | 'bad';
    loading?: boolean;
  } = $props();

  const toneClass = $derived({
    neutral: 'text-zinc-100',
    good: 'text-emerald-300',
    warn: 'text-amber-300',
    bad: 'text-rose-300'
  }[tone]);
</script>

<div class="rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3">
  <div class="text-[10px] uppercase tracking-wider text-zinc-500">{label}</div>
  {#if loading}
    <div class="mt-1.5 h-7 w-20 rounded shimmer"></div>
    {#if sub}
      <div class="mt-1.5 h-3 w-28 rounded shimmer opacity-70"></div>
    {/if}
  {:else}
    <div class="mt-1 text-2xl font-semibold numeric tabular-nums {toneClass}">{value}</div>
    {#if sub}
      <div class="text-[11px] text-zinc-500 numeric mt-0.5">{sub}</div>
    {/if}
  {/if}
</div>
