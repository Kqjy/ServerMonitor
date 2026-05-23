<script lang="ts">
  import { auth } from '$lib/auth.svelte';
  import { goto } from '$app/navigation';

  let username = $state('admin');
  let password = $state('');
  let confirm = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  async function submit(e: Event) {
    e.preventDefault();
    if (password.length < 8) {
      error = 'Password must be at least 8 characters';
      return;
    }
    if (password !== confirm) {
      error = 'Passwords do not match';
      return;
    }
    busy = true;
    error = null;
    try {
      await auth.setup(username, password);
      await goto('/');
    } catch (err) {
      error = (err as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="min-h-screen flex items-center justify-center px-6">
  <div class="w-full max-w-md">
    <div class="flex items-center gap-2 mb-8 justify-center">
      <div class="h-7 w-7 rounded-md bg-gradient-to-br from-emerald-400 to-sky-400 grid place-items-center text-zinc-950">
        <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2.5">
          <path d="M3 12h4l3-8 4 16 3-8h4" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </div>
      <span class="font-semibold tracking-tight text-zinc-100">ServerMonitor</span>
    </div>

    <div class="rounded-xl border border-zinc-800 bg-zinc-900/50 p-6">
      <h1 class="text-lg font-medium tracking-tight">Welcome</h1>
      <p class="mt-1 text-xs text-zinc-500">Create the admin account to finish setup. This only runs once.</p>

      <form onsubmit={submit} class="mt-5 space-y-4">
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="u">Username</label>
          <input
            id="u"
            type="text"
            autocomplete="username"
            bind:value={username}
            required
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="p">Password</label>
          <input
            id="p"
            type="password"
            autocomplete="new-password"
            bind:value={password}
            required
            minlength="8"
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
          <p class="mt-1 text-[11px] text-zinc-600">At least 8 characters</p>
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="c">Confirm password</label>
          <input
            id="c"
            type="password"
            autocomplete="new-password"
            bind:value={confirm}
            required
            minlength="8"
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
        </div>

        {#if error}
          <div class="rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
        {/if}

        <button
          type="submit"
          disabled={busy}
          class="w-full px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 text-sm font-medium">
          {busy ? 'Creating account…' : 'Create admin & continue'}
        </button>
      </form>
    </div>

    <p class="mt-6 text-center text-[11px] text-zinc-600 numeric">v0.1.0</p>
  </div>
</div>
