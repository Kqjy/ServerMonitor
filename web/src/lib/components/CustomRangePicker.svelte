<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { PRESETS, isPreset, rangeBoundsMs, type Range } from '$lib/time';

  let { value, onApply, onCancel }: {
    value: Range;
    onApply: (r: Range) => void;
    onCancel: () => void;
  } = $props();

  function toLocalInput(ms: number): string {
    const d = new Date(ms);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  const seed = untrack(() => rangeBoundsMs(value));
  let fromStr = $state(toLocalInput(seed.fromMs));
  let toStr = $state(toLocalInput(seed.toMs));
  let error = $state('');
  let popover: HTMLDivElement;

  const activePreset = $derived(isPreset(value) ? value : null);

  function applyCustom() {
    const f = new Date(fromStr).getTime();
    const t = new Date(toStr).getTime();
    if (!Number.isFinite(f) || !Number.isFinite(t)) {
      error = 'Invalid date or time';
      return;
    }
    if (f >= t) {
      error = '"From" must be before "To"';
      return;
    }
    if (t > Date.now() + 60_000) {
      error = '"To" cannot be in the future';
      return;
    }
    onApply({ fromMs: f, toMs: t });
  }

  function onDocClick(e: MouseEvent) {
    if (!popover) return;
    if (!popover.contains(e.target as Node)) onCancel();
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onCancel();
    if (e.key === 'Enter' && e.target instanceof HTMLInputElement) applyCustom();
  }

  onMount(() => {
    setTimeout(() => document.addEventListener('mousedown', onDocClick), 0);
    document.addEventListener('keydown', onKey);
  });
  onDestroy(() => {
    document.removeEventListener('mousedown', onDocClick);
    document.removeEventListener('keydown', onKey);
  });
</script>

<div
  bind:this={popover}
  role="dialog"
  aria-label="Time range"
  class="fixed inset-x-3 top-auto bottom-3 z-50 w-auto sm:absolute sm:inset-x-auto sm:bottom-auto sm:top-full sm:right-0 sm:mt-2 sm:w-80 rounded-lg border border-zinc-800 bg-zinc-950/95 backdrop-blur shadow-xl text-zinc-200">
  <div class="p-3 border-b border-zinc-800">
    <span class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-2">Absolute range</span>

    <label for="custom-range-from" class="block text-[10px] uppercase tracking-wider text-zinc-500 mb-1">From</label>
    <input
      id="custom-range-from"
      type="datetime-local"
      bind:value={fromStr}
      class="w-full mb-2 px-2 py-1.5 rounded-md border border-zinc-800 bg-zinc-900 text-sm text-zinc-100 font-mono focus:outline-none focus:border-zinc-600 numeric" />

    <label for="custom-range-to" class="block text-[10px] uppercase tracking-wider text-zinc-500 mb-1">To</label>
    <input
      id="custom-range-to"
      type="datetime-local"
      bind:value={toStr}
      class="w-full mb-2 px-2 py-1.5 rounded-md border border-zinc-800 bg-zinc-900 text-sm text-zinc-100 font-mono focus:outline-none focus:border-zinc-600 numeric" />

    {#if error}
      <p class="text-[11px] text-rose-400 mb-2">{error}</p>
    {/if}

    <div class="flex justify-end">
      <button type="button" onclick={applyCustom} class="px-3 py-1 rounded-md text-xs bg-zinc-100 text-zinc-900 hover:bg-zinc-200 transition-colors">Apply</button>
    </div>
  </div>

  <div class="p-2 max-h-64 overflow-y-auto">
    <span class="block px-2 pt-1 pb-1.5 text-[11px] uppercase tracking-wider text-zinc-500">Quick ranges</span>
    {#each PRESETS as p (p.id)}
      <button
        type="button"
        onclick={() => onApply(p.id)}
        class="w-full text-left px-2 py-1.5 rounded-md text-sm transition-colors {activePreset === p.id ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/60'}">
        {p.label}
      </button>
    {/each}
  </div>
</div>
