<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Host, type RetentionResp, type StorageResp } from '$lib/api';
  import { statusFor, timeAgo, bytes } from '$lib/format';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EditHostDialog from '$lib/components/EditHostDialog.svelte';

  let hosts = $state<Host[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let toRemove = $state<Host | null>(null);

  let editing = $state<Host | null>(null);

  let retention = $state<RetentionResp | null>(null);
  let retentionErr = $state<string | null>(null);

  let storage = $state<StorageResp | null>(null);
  let storageErr = $state<string | null>(null);
  let storageBusy = $state(false);

  async function loadStorage() {
    storageBusy = true;
    try {
      storage = await api.storage();
      storageErr = null;
    } catch (e) {
      storageErr = (e as Error).message;
    } finally {
      storageBusy = false;
    }
  }

  function sharePct(b: number): number {
    if (!storage || storage.tables_total_bytes <= 0) return 0;
    return Math.max(0, Math.min(100, (b / storage.tables_total_bytes) * 100));
  }

  function rowsLabel(n: number): string {
    return n > 0 ? `≈ ${n.toLocaleString()}` : '—';
  }

  function displayValue(v: string): string {
    const t = v.trim();
    if (t === '' || t.toLowerCase() === 'forever') return 'forever';
    return t;
  }

  function isModified(p: { configured: string; default: string }): boolean {
    return displayValue(p.configured) !== displayValue(p.default);
  }

  async function refresh() {
    try {
      hosts = await api.hosts();
      error = null;
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
    try {
      retention = await api.retention();
      retentionErr = null;
    } catch (e) {
      retentionErr = (e as Error).message;
    }
    await loadStorage();
  }

  async function doRemove() {
    if (!toRemove) return;
    try {
      await api.deleteHost(toRemove.id);
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  let pwCurrent = $state('');
  let pwNext = $state('');
  let pwConfirm = $state('');
  let pwError = $state<string | null>(null);
  let pwInfo = $state<string | null>(null);
  let pwBusy = $state(false);

  async function changePassword(e: Event) {
    e.preventDefault();
    pwError = null;
    pwInfo = null;
    if (pwNext.length < 8) {
      pwError = 'New password must be at least 8 characters.';
      return;
    }
    if (pwNext !== pwConfirm) {
      pwError = 'New password and confirmation do not match.';
      return;
    }
    pwBusy = true;
    try {
      await api.changePassword(pwCurrent, pwNext);
      pwCurrent = '';
      pwNext = '';
      pwConfirm = '';
      pwInfo = 'Password updated. Other sessions have been signed out.';
    } catch (err) {
      pwError = (err as Error).message;
    } finally {
      pwBusy = false;
    }
  }

  onMount(refresh);
</script>

<div class="max-w-3xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Settings</h1>
  <p class="text-xs sm:text-sm text-zinc-500 mt-1">Manage hosts and delivery</p>

  <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
    <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-2">
      <div class="min-w-0">
        <h2 class="text-sm font-medium text-zinc-100">Hosts</h2>
        <p class="text-[11px] text-zinc-500 mt-0.5">Each host owns a unique agent token.</p>
      </div>
      <a href="/hosts/new" class="text-xs px-2.5 py-1 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 shrink-0">
        Add a host
      </a>
    </header>
    {#if error}
      <div class="px-5 py-3 text-xs text-rose-300">{error}</div>
    {/if}
    {#if loading}
      <div class="px-5 py-6 text-center text-zinc-500 text-sm">Loading…</div>
    {:else if hosts.length === 0}
      <div class="px-5 py-8 text-center text-zinc-500 text-sm">
        No hosts yet. <a href="/hosts/new" class="text-emerald-400 hover:underline">Add one →</a>
      </div>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Host</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Platform</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Agent</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Last seen</th>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each hosts as h (h.id)}
              {@const s = statusFor(h.last_seen, h.sample_interval_s || 10)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-4 sm:px-5 py-2.5 whitespace-nowrap">
                  <div class="flex items-center gap-2">
                    <StatusDot status={s} />
                    <a href={`/hosts/${h.id}`} class="text-zinc-100 hover:underline">{h.hostname}</a>
                  </div>
                </td>
                <td class="px-3 py-2.5 text-zinc-400 font-mono text-xs whitespace-nowrap">{h.os || '—'}{h.arch ? ' · ' + h.arch : ''}</td>
                <td class="px-3 py-2.5 text-zinc-400 font-mono text-xs whitespace-nowrap">{h.agent_version || '—'}</td>
                <td class="px-3 py-2.5 text-zinc-400 text-xs numeric whitespace-nowrap">{timeAgo(h.last_seen)}</td>
                <td class="px-4 sm:px-5 py-2.5 text-xs whitespace-nowrap">
                  <div class="flex items-center gap-1">
                    <button
                      type="button"
                      aria-label="Edit"
                      title="Edit hostname and interval"
                      onclick={() => (editing = h)}
                      class="p-1.5 rounded-md hover:bg-zinc-800/60 text-zinc-400 hover:text-zinc-200">
                      <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M12 20h9" />
                        <path d="M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4 12.5-12.5Z" />
                      </svg>
                    </button>
                    <button type="button" onclick={() => (toRemove = h)} class="px-2 py-1 rounded-md hover:bg-rose-950/40 text-rose-300">Remove</button>
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
      <h2 class="text-sm font-medium text-zinc-100">Change password</h2>
      <p class="text-[11px] text-zinc-500 mt-0.5">Updates your admin login and signs out other sessions.</p>
    </header>
    <form onsubmit={changePassword} class="px-4 sm:px-5 py-4 space-y-3 max-w-sm">
      <label class="block">
        <span class="block text-xs text-zinc-400 mb-1">Current password</span>
        <input
          type="password"
          autocomplete="current-password"
          bind:value={pwCurrent}
          required
          class="w-full bg-zinc-950 border border-zinc-800 rounded-md px-3 py-2 text-sm text-zinc-100 focus:outline-none focus:border-zinc-600"
        />
      </label>
      <label class="block">
        <span class="block text-xs text-zinc-400 mb-1">New password</span>
        <input
          type="password"
          autocomplete="new-password"
          bind:value={pwNext}
          required
          minlength={8}
          class="w-full bg-zinc-950 border border-zinc-800 rounded-md px-3 py-2 text-sm text-zinc-100 focus:outline-none focus:border-zinc-600"
        />
      </label>
      <label class="block">
        <span class="block text-xs text-zinc-400 mb-1">Confirm new password</span>
        <input
          type="password"
          autocomplete="new-password"
          bind:value={pwConfirm}
          required
          minlength={8}
          class="w-full bg-zinc-950 border border-zinc-800 rounded-md px-3 py-2 text-sm text-zinc-100 focus:outline-none focus:border-zinc-600"
        />
      </label>
      {#if pwError}
        <div class="text-xs text-rose-300">{pwError}</div>
      {/if}
      {#if pwInfo}
        <div class="text-xs text-emerald-300">{pwInfo}</div>
      {/if}
      <button
        type="submit"
        disabled={pwBusy}
        class="text-xs px-3 py-1.5 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50"
      >
        {pwBusy ? 'Updating…' : 'Update password'}
      </button>
      <p class="text-[11px] text-zinc-500 pt-1">
        Locked out? Run <code class="text-zinc-300">sm-server reset-password -username admin -password &lt;new&gt;</code> on the host.
      </p>
    </form>
  </section>

  <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5 text-sm">
    <h2 class="text-sm font-medium text-zinc-100">Delivery</h2>
    <p class="mt-1 text-[11px] text-zinc-500">Where alerts get sent.</p>
    <div class="mt-3 space-y-1.5">
      <a href="/settings/channels" class="block text-zinc-300 hover:text-zinc-100">→ Notification channels (SMTP, webhook)</a>
      <a href="/alerts" class="block text-zinc-300 hover:text-zinc-100">→ Alert rules</a>
    </div>
  </section>

  <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
    <header class="px-4 sm:px-5 py-3 border-b border-zinc-800">
      <h2 class="text-sm font-medium text-zinc-100">Data retention</h2>
      <p class="text-[11px] text-zinc-500 mt-0.5">
        How long each tier of data is kept. Configured via environment variables on the server.
      </p>
    </header>
    {#if retentionErr}
      <div class="px-5 py-3 text-xs text-rose-300">{retentionErr}</div>
    {:else if !retention}
      <div class="px-5 py-6 text-center text-zinc-500 text-sm">Loading…</div>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Data</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Current</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Default</th>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Env var</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each retention.policies as p (p.key)}
              <tr>
                <td class="px-4 sm:px-5 py-2.5 align-top">
                  <div class="text-zinc-100 whitespace-nowrap">{p.label}</div>
                  <div class="text-[11px] text-zinc-500 mt-0.5 max-w-md">{p.description}</div>
                  <div class="text-[10px] text-zinc-600 font-mono mt-1 whitespace-nowrap">{p.target}</div>
                </td>
                <td class="px-3 py-2.5 align-top font-mono text-xs numeric whitespace-nowrap {isModified(p) ? 'text-emerald-300' : 'text-zinc-300'}">
                  {displayValue(p.configured)}
                </td>
                <td class="px-3 py-2.5 align-top font-mono text-xs numeric text-zinc-500 whitespace-nowrap">
                  {displayValue(p.default)}
                </td>
                <td class="px-4 sm:px-5 py-2.5 align-top font-mono text-[11px] text-zinc-400 whitespace-nowrap">
                  {p.env}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div class="px-4 sm:px-5 py-3 border-t border-zinc-800 text-[11px] text-zinc-500 space-y-1">
        <div>
          Accepted format: <span class="font-mono text-zinc-400">{retention.interval_format}</span>
        </div>
        {#if retention.requires_restart}
          <div>Changes take effect on the next server start.</div>
        {/if}
        <div>
          Raw data outside <span class="font-mono text-zinc-400">RETENTION_RAW</span> is summarised into 5-minute buckets, then archived to S3 (if configured) past the 5-minute window.
        </div>
      </div>
    {/if}
  </section>

  <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
    <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-2">
      <div class="min-w-0">
        <h2 class="text-sm font-medium text-zinc-100">Storage usage</h2>
        <p class="text-[11px] text-zinc-500 mt-0.5">
          Physical disk used by collected data right now.
          {#if storage}<span class="text-zinc-600">· measured {timeAgo(storage.captured_at)}</span>{/if}
        </p>
      </div>
      <button
        type="button"
        onclick={loadStorage}
        disabled={storageBusy}
        class="text-xs px-2.5 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50 shrink-0">
        {storageBusy ? 'Measuring…' : 'Refresh'}
      </button>
    </header>

    {#if storageErr}
      <div class="px-5 py-3 text-xs text-rose-300">{storageErr}</div>
    {:else if !storage}
      <div class="px-5 py-6 text-center text-zinc-500 text-sm">Loading…</div>
    {:else}
      <div class="grid grid-cols-1 sm:grid-cols-3 divide-y sm:divide-y-0 sm:divide-x divide-zinc-800 border-b border-zinc-800">
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Monitored data</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{bytes(storage.tables_total_bytes)}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">
            across {storage.tables.length} monitored {storage.tables.length === 1 ? 'table' : 'tables'}
          </div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Cold archive · S3</div>
          {#if storage.archive.configured}
            <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{bytes(storage.archive.total_bytes)}</div>
            <div class="text-[11px] text-zinc-500 mt-0.5">
              {#if storage.archive.objects > 0}
                {storage.archive.objects.toLocaleString()} {storage.archive.objects === 1 ? 'object' : 'objects'} · ≈ {storage.archive.row_count.toLocaleString()} rows
              {:else}
                configured · no archived objects yet
              {/if}
            </div>
          {:else}
            <div class="text-xl font-medium text-zinc-400 mt-1.5">Not configured</div>
            <div class="text-[11px] text-zinc-500 mt-0.5">set <span class="font-mono">ARCHIVE_S3_BUCKET</span> to archive cold data</div>
          {/if}
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Whole database</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{bytes(storage.database_bytes)}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">+ {bytes(storage.other_database_bytes)} other storage</div>
        </div>
      </div>

      {#if storage.warnings && storage.warnings.length > 0}
        <div class="px-4 sm:px-5 py-2.5 border-b border-zinc-800 bg-amber-500/5 text-[11px] text-amber-300/90 space-y-0.5">
          {#each storage.warnings as wmsg, i (i)}
            <div>{wmsg}</div>
          {/each}
        </div>
      {/if}

      {#if storage.tables.length === 0}
        <div class="px-5 py-8 text-center text-zinc-500 text-sm">No Timescale data tables found.</div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
              <tr>
                <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Data</th>
                <th class="text-right font-medium px-3 py-2.5 whitespace-nowrap">On disk</th>
                <th class="text-left font-medium px-3 py-2.5 w-44">Share</th>
                <th class="text-right font-medium px-3 py-2.5 whitespace-nowrap">Rows</th>
                <th class="text-right font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Compression</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each storage.tables as t (t.schema + '.' + t.name)}
                <tr class="hover:bg-zinc-900/60">
                  <td class="px-4 sm:px-5 py-2.5 align-top">
                    <div class="text-zinc-100 whitespace-nowrap">{t.label}</div>
                    <div class="text-[10px] text-zinc-600 font-mono mt-0.5 whitespace-nowrap">{t.name} · {t.kind}</div>
                  </td>
                  <td class="px-3 py-2.5 align-top text-right font-mono text-xs numeric text-zinc-200 whitespace-nowrap">{bytes(t.total_bytes)}</td>
                  <td class="px-3 py-2.5 align-middle">
                    <div class="flex items-center gap-2">
                      <div class="h-1.5 flex-1 rounded-full bg-zinc-800 overflow-hidden">
                        <div class="h-full rounded-full bg-sky-500/70" style="width: {sharePct(t.total_bytes)}%"></div>
                      </div>
                      <span class="text-[10px] text-zinc-500 numeric w-9 text-right">{sharePct(t.total_bytes).toFixed(0)}%</span>
                    </div>
                  </td>
                  <td class="px-3 py-2.5 align-top text-right font-mono text-xs numeric text-zinc-400 whitespace-nowrap">{rowsLabel(t.approx_rows)}</td>
                  <td class="px-4 sm:px-5 py-2.5 align-top text-right whitespace-nowrap">
                    {#if t.compression_ratio > 1}
                      <span class="font-mono text-xs numeric text-emerald-300" title="compressed chunks: {bytes(t.uncompressed_bytes)} uncompressed → {bytes(t.uncompressed_bytes / t.compression_ratio)} on disk">{t.compression_ratio.toFixed(1)}×</span>
                    {:else}
                      <span class="font-mono text-xs text-zinc-600">uncompressed</span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <div class="px-4 sm:px-5 py-3 border-t border-zinc-800 text-[11px] text-zinc-500 space-y-1">
          <div>Sizes are physical on-disk bytes (compressed where applicable). Share is each table's portion of monitored data.</div>
          <div>
            <span class="text-zinc-400">Other storage</span> ({bytes(storage.other_database_bytes)}) is application tables, PostgreSQL catalogs, the audit log, and database overhead outside the monitored tables.
          </div>
          <div>Lowering a table's window in Data retention above shrinks it. Open-port snapshots have no retention policy yet, so they are the main unbounded grower.</div>
        </div>
      {/if}
    {/if}
  </section>

  {#snippet removeBody()}
    {#if toRemove}
      {@const rs = statusFor(toRemove.last_seen, toRemove.sample_interval_s || 10)}
      <div class="flex items-center gap-2 mb-3">
        <StatusDot status={rs} />
        <span class="font-medium text-zinc-100">{toRemove.hostname}</span>
        <span class="text-xs text-zinc-500 numeric">· seen {timeAgo(toRemove.last_seen)}</span>
      </div>
      <p>This stops accepting metrics from the agent immediately and drops the stored history.</p>
      <p class="mt-2 text-zinc-400">
        {#if rs === 'good'}
          The agent is currently live. It will self-terminate on its next upload attempt.
        {:else if rs === 'warn'}
          The agent was reporting recently. If it comes back online, it will exit on its next upload.
        {:else}
          The agent is offline. If it ever reconnects, it will exit immediately.
        {/if}
      </p>
    {/if}
  {/snippet}

  <ConfirmDialog
    open={toRemove !== null}
    title="Delete host"
    body={removeBody}
    confirmLabel="Delete"
    danger
    onconfirm={doRemove}
    onclose={() => (toRemove = null)}
  />

  {#if editing}
    <EditHostDialog
      host={editing}
      onclose={() => (editing = null)}
      onsaved={async () => { editing = null; await refresh(); }}
    />
  {/if}
</div>
