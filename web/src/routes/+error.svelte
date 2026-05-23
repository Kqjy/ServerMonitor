<script lang="ts">
  import { page } from '$app/stores';
  import { goto, invalidateAll } from '$app/navigation';

  const status = $derived($page.status);
  const message = $derived($page.error?.message ?? '');

  const kind = $derived(
    status >= 500 ? 'server' : status === 404 ? 'missing' : status >= 400 ? 'client' : 'unknown'
  );

  const heading = $derived(
    {
      server: 'Something went wrong on the server',
      missing: 'Page not found',
      client: 'Request rejected',
      unknown: 'Unexpected error'
    }[kind]
  );

  const subhead = $derived(
    {
      server: 'The server encountered an error handling this request. The page itself is fine — retrying often works.',
      missing: 'The page you were looking for doesn’t exist or has been moved.',
      client: 'The server couldn’t process this request as given.',
      unknown: 'An unexpected error occurred.'
    }[kind]
  );

  const accent = $derived(
    {
      server: { ring: 'ring-rose-500/20', text: 'text-rose-300', bg: 'bg-rose-500/10', dot: 'bg-rose-400' },
      missing: { ring: 'ring-zinc-700/40', text: 'text-zinc-300', bg: 'bg-zinc-800/40', dot: 'bg-zinc-500' },
      client: { ring: 'ring-amber-500/20', text: 'text-amber-300', bg: 'bg-amber-500/10', dot: 'bg-amber-400' },
      unknown: { ring: 'ring-zinc-700/40', text: 'text-zinc-300', bg: 'bg-zinc-800/40', dot: 'bg-zinc-500' }
    }[kind]
  );

  async function retry() {
    await invalidateAll();
  }

  function home() {
    void goto('/');
  }
</script>

<div class="min-h-[calc(100vh-3.5rem-3rem)] grid place-items-center px-6 py-12">
  <div class="w-full max-w-lg">
    <div class="rounded-xl border border-zinc-800 bg-zinc-900/50 ring-1 {accent.ring} overflow-hidden">
      <div class="px-6 pt-6 pb-5 flex items-start gap-4">
        <div class="shrink-0 h-10 w-10 rounded-lg {accent.bg} grid place-items-center">
          {#if kind === 'missing'}
            <svg viewBox="0 0 24 24" class="h-5 w-5 {accent.text}" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <circle cx="11" cy="11" r="7" />
              <path d="m20 20-3.5-3.5" />
            </svg>
          {:else if kind === 'server'}
            <svg viewBox="0 0 24 24" class="h-5 w-5 {accent.text}" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <rect x="3" y="4" width="18" height="7" rx="1.5" />
              <rect x="3" y="13" width="18" height="7" rx="1.5" />
              <path d="M7 7.5h.01M7 16.5h.01" />
              <path d="M17 17.5l3 3M20 17.5l-3 3" />
            </svg>
          {:else}
            <svg viewBox="0 0 24 24" class="h-5 w-5 {accent.text}" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 9v4" />
              <path d="M12 17h.01" />
              <path d="M10.3 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z" />
            </svg>
          {/if}
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2 text-xs text-zinc-500">
            <span class="inline-flex items-center gap-1.5">
              <span class="h-1.5 w-1.5 rounded-full {accent.dot}"></span>
              <span class="numeric">Error {status}</span>
            </span>
          </div>
          <h1 class="mt-1 text-base font-medium tracking-tight text-zinc-100">{heading}</h1>
          <p class="mt-1.5 text-sm text-zinc-400 leading-relaxed">{subhead}</p>
        </div>
      </div>

      {#if message && message !== 'Internal Error'}
        <div class="px-6 pb-5">
          <div class="rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2.5">
            <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">Details</div>
            <div class="text-xs text-zinc-300 font-mono break-words">{message}</div>
          </div>
        </div>
      {/if}

      <div class="px-6 py-4 border-t border-zinc-800 bg-zinc-950/40 flex items-center justify-between gap-3">
        <button
          type="button"
          onclick={home}
          class="text-xs text-zinc-400 hover:text-zinc-200 px-2.5 py-1.5 rounded-md hover:bg-zinc-800/40 transition-colors inline-flex items-center gap-1.5">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="m15 18-6-6 6-6" />
          </svg>
          Back to Hosts
        </button>
        <button
          type="button"
          onclick={retry}
          class="text-xs font-medium text-zinc-100 px-3 py-1.5 rounded-md bg-zinc-800 hover:bg-zinc-700 ring-1 ring-zinc-700/60 transition-colors inline-flex items-center gap-1.5">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M3 12a9 9 0 0 1 15.5-6.3L21 8" />
            <path d="M21 3v5h-5" />
            <path d="M21 12a9 9 0 0 1-15.5 6.3L3 16" />
            <path d="M3 21v-5h5" />
          </svg>
          Retry
        </button>
      </div>
    </div>
  </div>
</div>
