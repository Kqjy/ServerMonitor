<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    open,
    title,
    message,
    body,
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    danger = false,
    onconfirm,
    onclose
  }: {
    open: boolean;
    title: string;
    message?: string;
    body?: Snippet;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    onconfirm: () => void | Promise<void>;
    onclose: () => void;
  } = $props();

  let busy = $state(false);

  function close() {
    if (busy) return;
    onclose();
  }

  async function confirm() {
    busy = true;
    try {
      await onconfirm();
      onclose();
    } finally {
      busy = false;
    }
  }
</script>

{#if open}
  <div
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    class="fixed inset-0 z-40 bg-zinc-950/60 backdrop-blur-sm flex items-center justify-center p-4"
    onclick={(e) => { if (e.target === e.currentTarget) close(); }}
    onkeydown={(e) => { if (e.key === 'Escape') close(); }}
  >
    <div class="w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-900 shadow-2xl overflow-hidden">
      <header class="px-5 py-3 border-b border-zinc-800">
        <h2 class="text-sm font-medium text-zinc-100">{title}</h2>
      </header>
      <div class="px-5 py-4 text-sm text-zinc-300">
        {#if body}
          {@render body()}
        {:else}
          {message}
        {/if}
      </div>
      <footer class="px-5 py-3 border-t border-zinc-800 flex items-center justify-end gap-2">
        <button
          type="button"
          onclick={close}
          disabled={busy}
          class="text-xs px-3 py-1.5 rounded-md text-zinc-300 hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-50"
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          onclick={confirm}
          disabled={busy}
          class={danger
            ? 'text-xs px-3 py-1.5 rounded-md bg-rose-500/20 border border-rose-500/40 text-rose-200 hover:bg-rose-500/30 disabled:opacity-50'
            : 'text-xs px-3 py-1.5 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50'}
        >
          {busy ? 'Working…' : confirmLabel}
        </button>
      </footer>
    </div>
  </div>
{/if}
