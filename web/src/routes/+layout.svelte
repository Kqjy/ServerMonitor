<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';
  import { beforeNavigate, goto } from '$app/navigation';
  import { updated } from '$app/state';
  import { auth } from '$lib/auth.svelte';

  let { children } = $props();

  const isPublic = $derived(
    $page.url.pathname === '/login' || $page.url.pathname === '/setup'
  );

  beforeNavigate(({ willUnload, to }) => {
    if (updated.current && !willUnload && to?.url) {
      location.href = to.url.href;
    }
  });

  $effect(() => {
    if (auth.status === 'needs-setup' && $page.url.pathname !== '/setup') {
      void goto('/setup');
    } else if (auth.status === 'guest' && !isPublic) {
      void goto('/login');
    } else if (auth.status === 'authed' && isPublic) {
      void goto('/');
    }
  });
</script>

{#if updated.current}
  <div class="fixed inset-x-0 top-0 z-50 flex items-center justify-center gap-3 border-b border-amber-800/60 bg-amber-950 px-4 py-2 text-xs text-amber-100" role="status">
    <span>A newer ServerMonitor version is ready.</span>
    <button type="button" onclick={() => location.reload()} class="rounded border border-amber-600/70 px-2 py-1 font-medium hover:bg-amber-900">Reload now</button>
  </div>
{/if}

{#if isPublic}
  {@render children?.()}
{:else if auth.status === 'authed'}
  <div class="min-h-screen flex flex-col">
    <header class="border-b border-zinc-800 bg-zinc-950/80 backdrop-blur sticky top-0 z-20">
      <div class="max-w-7xl mx-auto px-4 sm:px-6 h-14 flex items-center gap-3 sm:gap-8">
        <a href="/" class="flex items-center gap-2 group shrink-0">
          <div class="h-6 w-6 rounded-md bg-gradient-to-br from-emerald-400 to-sky-400 grid place-items-center text-zinc-950">
            <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2.5">
              <path d="M3 12h4l3-8 4 16 3-8h4" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </div>
          <span class="font-semibold tracking-tight text-zinc-100 hidden sm:inline">ServerMonitor</span>
        </a>
        <nav class="flex items-center gap-0.5 sm:gap-1 text-sm min-w-0">
          {#each [{ href: '/', label: 'Hosts' }, { href: '/alerts', label: 'Alerts' }, { href: '/backups', label: 'Backups' }, { href: '/settings', label: 'Settings' }] as item (item.href)}
            <a
              href={item.href}
              class="px-2 sm:px-3 py-1.5 rounded-md transition-colors {$page.url.pathname === item.href || (item.href !== '/' && $page.url.pathname.startsWith(item.href)) ? 'text-zinc-100 bg-zinc-800/60' : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40'}">
              {item.label}
            </a>
          {/each}
        </nav>
        <div class="ml-auto flex items-center gap-2 sm:gap-3 shrink-0">
          <span class="text-xs text-zinc-500 numeric hidden md:inline">{auth.user?.username}</span>
          <button
            type="button"
            onclick={() => auth.logout()}
            aria-label="Sign out"
            class="text-xs text-zinc-400 hover:text-zinc-200 px-2 sm:px-2.5 py-1 rounded-md hover:bg-zinc-800/40 transition-colors inline-flex items-center gap-1.5">
            <svg viewBox="0 0 24 24" class="h-4 w-4 sm:hidden" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
              <path d="M16 17l5-5-5-5" />
              <path d="M21 12H9" />
            </svg>
            <span class="hidden sm:inline">Sign out</span>
          </button>
        </div>
      </div>
    </header>

    <main class="flex-1">
      {@render children?.()}
    </main>

    <footer class="border-t border-zinc-800 py-4 text-xs text-zinc-500 text-center">
      <div class="max-w-7xl mx-auto px-4 sm:px-6">
        ServerMonitor — Unified Server Monitoring {#if auth.serverVersion}| <span class="numeric">v{auth.serverVersion}</span>{/if}
      </div>
    </footer>
  </div>
{:else}
  <div class="min-h-screen grid place-items-center text-zinc-500 text-sm">Loading…</div>
{/if}
