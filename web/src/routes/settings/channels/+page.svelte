<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Channel } from '$lib/api';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let channels = $state<Channel[]>([]);
  let loading = $state(true);

  let formKind = $state<'smtp' | 'webhook'>('webhook');
  let formName = $state('');
  let busy = $state(false);
  let formError = $state<string | null>(null);

  let smtpHost = $state('');
  let smtpPort = $state(587);
  let smtpUser = $state('');
  let smtpPass = $state('');
  let smtpFrom = $state('');
  let smtpTo = $state('');

  let webhookUrl = $state('');
  let webhookFormat = $state<'generic' | 'discord' | 'slack' | 'ntfy'>('generic');

  async function refresh() {
    channels = await api.channels();
    loading = false;
  }

  function resetForm() {
    formName = '';
    smtpHost = ''; smtpPort = 587; smtpUser = ''; smtpPass = ''; smtpFrom = ''; smtpTo = '';
    webhookUrl = ''; webhookFormat = 'generic';
  }

  async function create(e: Event) {
    e.preventDefault();
    if (!formName.trim()) {
      formError = 'Name is required';
      return;
    }
    busy = true;
    formError = null;
    try {
      let config: unknown;
      if (formKind === 'smtp') {
        config = {
          host: smtpHost.trim(),
          port: Number(smtpPort) || 587,
          username: smtpUser,
          password: smtpPass,
          from: smtpFrom.trim(),
          to: smtpTo.split(',').map((s) => s.trim()).filter(Boolean),
          starttls: true
        };
      } else {
        config = { url: webhookUrl.trim(), format: webhookFormat };
      }
      await api.channelCreate({ name: formName.trim(), kind: formKind, config, enabled: true });
      resetForm();
      await refresh();
    } catch (err) {
      formError = (err as Error).message;
    } finally {
      busy = false;
    }
  }

  async function toggle(c: Channel) {
    await api.channelUpdate(c.id, { name: c.name, kind: c.kind, config: c.config, enabled: !c.enabled });
    await refresh();
  }

  let toRemove = $state<Channel | null>(null);

  async function doRemove() {
    if (!toRemove) return;
    await api.channelDelete(toRemove.id);
    await refresh();
  }

  onMount(refresh);
</script>

<div class="max-w-3xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="mb-2">
    <a href="/settings" class="text-xs text-zinc-500 hover:text-zinc-300">← Settings</a>
  </div>
  <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Notification channels</h1>
  <p class="text-xs sm:text-sm text-zinc-500 mt-1">SMTP and webhook endpoints used by alert rules</p>

  <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
    <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex items-center justify-between">
      <h2 class="text-sm font-medium text-zinc-100">Existing</h2>
      <span class="text-xs text-zinc-500 numeric">{channels.length}</span>
    </header>
    {#if loading}
      <div class="px-5 py-6 text-center text-zinc-500 text-sm">Loading…</div>
    {:else if channels.length === 0}
      <div class="px-5 py-6 text-center text-zinc-500 text-sm">No channels yet</div>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Name</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Kind</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Enabled</th>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each channels as c (c.id)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-4 sm:px-5 py-2 text-zinc-100 font-mono text-xs whitespace-nowrap">{c.name}</td>
                <td class="px-3 py-2 text-zinc-400 text-xs uppercase whitespace-nowrap">{c.kind}</td>
                <td class="px-3 py-2 text-xs whitespace-nowrap">
                  {#if c.enabled}<span class="text-emerald-400">on</span>{:else}<span class="text-zinc-500">off</span>{/if}
                </td>
                <td class="px-4 sm:px-5 py-2 text-xs whitespace-nowrap">
                  <div class="flex items-center gap-1">
                    <button type="button" onclick={() => toggle(c)} class="px-2 py-1 rounded-md hover:bg-zinc-800/60 text-zinc-300">{c.enabled ? 'Disable' : 'Enable'}</button>
                    <button type="button" onclick={() => (toRemove = c)} class="px-2 py-1 rounded-md hover:bg-rose-950/40 text-rose-300">Delete</button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="px-4 sm:px-5 py-3 border-b border-zinc-800">
      <h2 class="text-sm font-medium text-zinc-100">Add channel</h2>
    </header>
    <form onsubmit={create} class="p-4 sm:p-5 space-y-4">
      <div class="flex items-center gap-1 text-xs">
        {#each ['webhook', 'smtp'] as k (k)}
          <button
            type="button"
            onclick={() => (formKind = k as 'smtp' | 'webhook')}
            class="px-3 py-1.5 rounded-md transition-colors {formKind === k ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
            {k}
          </button>
        {/each}
      </div>

      <div>
        <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="cname">Name</label>
        <input id="cname" bind:value={formName} required class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" placeholder="discord-alerts" />
      </div>

      {#if formKind === 'smtp'}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="shost">Host</label>
            <input id="shost" bind:value={smtpHost} required class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
          <div>
            <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="sport">Port</label>
            <input id="sport" type="number" bind:value={smtpPort} class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm numeric" />
          </div>
          <div>
            <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="suser">Username</label>
            <input id="suser" bind:value={smtpUser} class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
          <div>
            <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="spass">Password</label>
            <input id="spass" type="password" bind:value={smtpPass} class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
          <div>
            <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="sfrom">From</label>
            <input id="sfrom" bind:value={smtpFrom} required class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
          <div>
            <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="sto">To (comma separated)</label>
            <input id="sto" bind:value={smtpTo} required class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
        </div>
      {:else}
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="wurl">URL</label>
          <input id="wurl" bind:value={webhookUrl} required type="url" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" placeholder="https://discord.com/api/webhooks/..." />
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="wfmt">Format</label>
          <select id="wfmt" bind:value={webhookFormat} class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm">
            <option value="generic">generic JSON</option>
            <option value="discord">Discord</option>
            <option value="slack">Slack</option>
            <option value="ntfy">ntfy</option>
          </select>
        </div>
      {/if}

      {#if formError}
        <div class="rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{formError}</div>
      {/if}

      <div class="flex justify-end">
        <button type="submit" disabled={busy} class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50">
          {busy ? 'Adding…' : 'Add channel'}
        </button>
      </div>
    </form>
  </section>

  <ConfirmDialog
    open={toRemove !== null}
    title="Delete channel"
    message={toRemove ? `Delete channel "${toRemove.name}"? Alert rules referencing it will lose this delivery target.` : ''}
    confirmLabel="Delete"
    danger
    onconfirm={doRemove}
    onclose={() => (toRemove = null)}
  />
</div>
