<script lang="ts">
  import { untrack } from 'svelte';
  import { api, type Host } from '$lib/api';
  import { modalFocus } from '$lib/modal';

  let { host, onclose, onsaved }: {
    host: Host;
    onclose: () => void;
    onsaved: (h: Host) => void;
  } = $props();

  let name = $state(untrack(() => host.hostname));
  let interval = $state(untrack(() => host.sample_interval_s || 10));
  let autoUpgrade = $state(untrack(() => host.auto_upgrade));
  let error = $state<string | null>(null);
  let busy = $state(false);

  const intervalOptions = [5, 10, 30, 60];

  async function save(e: Event) {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      error = 'Hostname cannot be empty';
      return;
    }
    busy = true;
    error = null;
    try {
      const patch: { hostname?: string; sample_interval_s?: number; auto_upgrade?: boolean } = {};
      if (trimmed !== host.hostname) patch.hostname = trimmed;
      if (interval !== (host.sample_interval_s || 10)) patch.sample_interval_s = interval;
      if (autoUpgrade !== host.auto_upgrade) patch.auto_upgrade = autoUpgrade;
      if (patch.hostname === undefined && patch.sample_interval_s === undefined && patch.auto_upgrade === undefined) {
        onclose();
        return;
      }
      const updated = await api.updateHost(host.id, patch);
      onsaved(updated);
    } catch (err) {
      error = (err as Error).message;
    } finally {
      busy = false;
    }
  }

  function maybeClose() {
    if (!busy) onclose();
  }
</script>

<div
  role="dialog"
  aria-modal="true"
  tabindex="-1"
  use:modalFocus
  class="fixed inset-0 z-40 bg-zinc-950/60 backdrop-blur-sm flex items-center justify-center p-4"
  onclick={(e) => { if (e.target === e.currentTarget) maybeClose(); }}
  onkeydown={(e) => { if (e.key === 'Escape') maybeClose(); }}
>
  <form
    onsubmit={save}
    class="w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-900 shadow-2xl overflow-hidden"
  >
    <header class="px-5 py-3 border-b border-zinc-800">
      <h2 class="text-sm font-medium text-zinc-100">Edit host</h2>
    </header>
    <div class="px-5 py-4 space-y-4">
      <div>
        <label for="edit-hostname" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Hostname</label>
        <input
          id="edit-hostname"
          type="text"
          bind:value={name}
          autocomplete="off"
          spellcheck="false"
          required
          class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono"
        />
      </div>
      <div>
        <div class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Sample interval</div>
        <div class="flex items-center gap-1 text-xs">
          {#each intervalOptions as s (s)}
            <button
              type="button"
              onclick={() => (interval = s)}
              class="px-3 py-1.5 rounded-md transition-colors numeric {interval === s ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
              {s}s
            </button>
          {/each}
        </div>
      </div>
      <div>
        <div class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Auto-update agent</div>
        <label class="flex items-start gap-3 {host.externally_managed ? 'cursor-not-allowed' : 'cursor-pointer'}">
          <input
            type="checkbox"
            bind:checked={autoUpgrade}
            disabled={host.externally_managed}
            class="mt-0.5 h-4 w-4 rounded border-zinc-700 bg-zinc-950 text-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed"
          />
          <span class="text-xs text-zinc-300 leading-relaxed">
            {#if host.externally_managed}
              <span class="text-zinc-400">This agent runs from a container image and updates by redeploying a new image tag; auto-update does not apply.</span>
            {:else}
              {autoUpgrade ? 'On' : 'Off'} — when on, this agent installs new versions automatically after the server is upgraded.
              {#if !host.supports_remote_upgrade && host.agent_version}
                <span class="block mt-1 text-amber-400">Agent v{host.agent_version} does not support remote upgrades; re-install to v0.1.1+ first.</span>
              {/if}
            {/if}
          </span>
        </label>
      </div>
      {#if error}
        <div class="rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
      {/if}
    </div>
    <footer class="px-5 py-3 border-t border-zinc-800 flex items-center justify-end gap-2">
      <button
        type="button"
        onclick={maybeClose}
        disabled={busy}
        class="text-xs px-3 py-1.5 rounded-md text-zinc-300 hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-50"
      >
        Cancel
      </button>
      <button
        type="submit"
        disabled={busy}
        class="text-xs px-3 py-1.5 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50"
      >
        {busy ? 'Saving…' : 'Save'}
      </button>
    </footer>
  </form>
</div>
