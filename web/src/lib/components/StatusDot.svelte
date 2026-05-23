<script lang="ts">
  let { status, size = 'sm', inline = false }: {
    status: 'good' | 'warn' | 'bad' | 'idle';
    size?: 'sm' | 'md' | 'lg';
    inline?: boolean;
  } = $props();
  const color = $derived({
    good: 'bg-emerald-400 shadow-[0_0_12px_oklch(0.78_0.16_162/0.6)]',
    warn: 'bg-amber-400 shadow-[0_0_10px_oklch(0.83_0.18_85/0.5)]',
    bad: 'bg-rose-400 shadow-[0_0_10px_oklch(0.7_0.21_22/0.5)]',
    idle: 'bg-zinc-600'
  }[status]);
  const sizeClass = $derived({
    sm: 'h-2 w-2',
    md: 'h-2.5 w-2.5',
    lg: 'h-3 w-3'
  }[size]);
</script>

<span
  class={`inline-block ${sizeClass} rounded-full ${color} ${inline ? 'align-middle' : ''}`}
  aria-label={status}
></span>
