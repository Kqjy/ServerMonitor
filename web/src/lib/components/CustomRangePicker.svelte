<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';

  let { fromMs, toMs, onApply, onCancel }: {
    fromMs?: number;
    toMs?: number;
    onApply: (from: number, to: number) => void;
    onCancel: () => void;
  } = $props();

  function toLocalInput(ms: number): string {
    const d = new Date(ms);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  const seedTo = untrack(() => toMs ?? Date.now());
  const seedFrom = untrack(() => fromMs ?? seedTo - 60 * 60_000);

  let fromStr = $state(toLocalInput(seedFrom));
  let toStr = $state(toLocalInput(seedTo));
  let error = $state('');
  let popover: HTMLDivElement;

  function apply() {
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
    onApply(f, t);
  }

  function setQuick(durationMs: number, endMs: number = Date.now()) {
    fromStr = toLocalInput(endMs - durationMs);
    toStr = toLocalInput(endMs);
    error = '';
  }

  function onDocClick(e: MouseEvent) {
    if (!popover) return;
    if (!popover.contains(e.target as Node)) onCancel();
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onCancel();
    if (e.key === 'Enter' && e.target instanceof HTMLInputElement) apply();
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
  aria-label="Custom time range"
  class="fixed inset-x-3 top-auto bottom-3 z-50 w-auto sm:absolute sm:inset-x-auto sm:bottom-auto sm:top-full sm:right-0 sm:mt-2 sm:w-80 rounded-lg border border-zinc-800 bg-zinc-950/95 backdrop-blur p-3 shadow-xl text-zinc-200">
  <div class="flex items-center justify-between mb-2">
    <span class="text-[11px] uppercase tracking-wider text-zinc-500">Custom range</span>
    <div class="flex gap-1">
      <button type="button" onclick={() => setQuick(60 * 60_000)} class="px-2 py-0.5 rounded text-[10px] uppercase tracking-wider text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/60 transition-colors">1h</button>
      <button type="button" onclick={() => setQuick(24 * 60 * 60_000)} class="px-2 py-0.5 rounded text-[10px] uppercase tracking-wider text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/60 transition-colors">24h</button>
      <button type="button" onclick={() => { const y = new Date(); y.setHours(0,0,0,0); setQuick(24*60*60_000, y.getTime()); }} class="px-2 py-0.5 rounded text-[10px] uppercase tracking-wider text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/60 transition-colors">Yesterday</button>
    </div>
  </div>

  <label for="custom-range-from" class="block text-[10px] uppercase tracking-wider text-zinc-500 mb-1">From</label>
  <input
    id="custom-range-from"
    type="datetime-local"
    bind:value={fromStr}
    class="w-full mb-3 px-2 py-1.5 rounded-md border border-zinc-800 bg-zinc-900 text-sm text-zinc-100 font-mono focus:outline-none focus:border-zinc-600 numeric" />

  <label for="custom-range-to" class="block text-[10px] uppercase tracking-wider text-zinc-500 mb-1">To</label>
  <input
    id="custom-range-to"
    type="datetime-local"
    bind:value={toStr}
    class="w-full mb-3 px-2 py-1.5 rounded-md border border-zinc-800 bg-zinc-900 text-sm text-zinc-100 font-mono focus:outline-none focus:border-zinc-600 numeric" />

  {#if error}
    <p class="text-[11px] text-rose-400 mb-2">{error}</p>
  {/if}

  <div class="flex justify-end gap-2">
    <button type="button" onclick={onCancel} class="px-3 py-1 rounded-md text-xs text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/60 transition-colors">Cancel</button>
    <button type="button" onclick={apply} class="px-3 py-1 rounded-md text-xs bg-zinc-100 text-zinc-900 hover:bg-zinc-200 transition-colors">Apply</button>
  </div>
</div>
