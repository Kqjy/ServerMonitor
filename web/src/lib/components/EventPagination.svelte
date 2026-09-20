<script lang="ts">
  import type { EventPager } from '$lib/ipban.svelte';

  let { pager }: { pager: EventPager } = $props();
</script>

{#if pager.rows.length > pager.pageSizes[0] || pager.more || pager.historyCapped}
  <div class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-2.5 border-t border-zinc-800">
    <div class="flex items-center gap-2">
      <div class="text-[10px] uppercase tracking-wider text-zinc-500">Rows</div>
      <div class="flex items-center gap-0.5">
        {#each pager.pageSizes as size (size)}
          <button
            type="button"
            onclick={() => pager.setPageSize(size)}
            aria-pressed={pager.pageSize === size}
            class="px-2 py-1 rounded-md text-xs font-medium numeric transition-colors {pager.pageSize === size ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">{size}</button>
        {/each}
      </div>
    </div>
    <div class="flex flex-wrap items-center justify-end gap-2">
      {#if pager.historyCapped}<span class="text-xs text-amber-300">Showing the newest {pager.rows.length}; use CSV for full history.</span>{/if}
      {#if pager.loadError}<span class="text-xs text-rose-300">{pager.loadError}</span>{/if}
      <div class="text-xs text-zinc-500 numeric">
        {pager.from + 1}–{pager.to} of {pager.rows.length}{pager.more ? '+' : ''} · page {pager.index + 1} of {pager.pageCount}{pager.more ? '+' : ''}
      </div>
      <button
        type="button"
        onclick={() => pager.goto(pager.index - 1)}
        disabled={pager.index === 0}
        aria-label="Newer events"
        title="Newer"
        class="inline-flex items-center rounded-md border border-zinc-800 px-2 py-1.5 text-zinc-400 transition-colors hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-35 disabled:hover:text-zinc-400 disabled:hover:bg-transparent">
        <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
          <path d="M8 2.25 4.25 6 8 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
      <button
        type="button"
        onclick={() => pager.goto(pager.index + 1)}
        disabled={pager.atOldest || pager.loadingOlder}
        aria-label="Older events"
        title={pager.loadingOlder ? 'Loading older events…' : 'Older'}
        class="inline-flex items-center rounded-md border border-zinc-800 px-2 py-1.5 text-zinc-400 transition-colors hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-35 disabled:hover:text-zinc-400 disabled:hover:bg-transparent">
        <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
          <path d="M4 2.25 7.75 6 4 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
    </div>
  </div>
{/if}
