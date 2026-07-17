<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type AlertRule, type AlertHistoryRow, type Channel } from '$lib/api';
  import { timeAgo, severityClass } from '$lib/format';
  import AlertRuleEditor from '$lib/components/AlertRuleEditor.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let rules = $state<AlertRule[]>([]);
  let history = $state<AlertHistoryRow[]>([]);
  let channels = $state<Channel[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let editing = $state<AlertRule | null>(null);
  let creating = $state(false);
  let timer: ReturnType<typeof setInterval> | null = null;
  let clearOpen = $state(false);
  let clearOlderThanDays = $state(0);
  let historyNotice = $state<string | null>(null);
  let historyNoticeTimer: ReturnType<typeof setTimeout> | null = null;

  async function refresh() {
    try {
      [rules, history, channels] = await Promise.all([
        api.alertRules(),
        api.alertHistory(50),
        api.channels()
      ]);
      error = null;
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function toggleRule(r: AlertRule) {
    try {
      await api.alertUpdate(r.id, { ...r, enabled: !r.enabled });
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  let toDelete = $state<AlertRule | null>(null);

  async function doDelete() {
    if (!toDelete) return;
    await api.alertDelete(toDelete.id);
    await refresh();
  }

  async function clearHistory() {
    const result = await api.alertHistoryClear(clearOlderThanDays || undefined);
    await refresh();
    historyNotice = `Cleared ${result.deleted} ${result.deleted === 1 ? 'entry' : 'entries'}`;
    if (historyNoticeTimer) clearTimeout(historyNoticeTimer);
    historyNoticeTimer = setTimeout(() => {
      historyNotice = null;
      historyNoticeTimer = null;
    }, 6_000);
  }

  onMount(() => {
    refresh();
    timer = setInterval(refresh, 15_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (historyNoticeTimer) clearTimeout(historyNoticeTimer);
  });
</script>

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex flex-wrap items-end justify-between gap-3 mb-5 sm:mb-6">
    <div class="min-w-0">
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Alerts</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1">Threshold rules and recent alerts</p>
    </div>
    <button
      type="button"
      onclick={() => (creating = true)}
      class="text-sm px-3.5 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 transition-colors shrink-0">
      New rule
    </button>
  </div>

  {#if error && !loading}
    <div class="mb-4 rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      {error}
    </div>
  {/if}

  {#if loading}
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <div class="h-40 rounded-lg border border-zinc-800 shimmer"></div>
      <div class="h-40 rounded-lg border border-zinc-800 shimmer"></div>
    </div>
  {:else if rules.length === 0 && !error}
    <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-12 text-center">
      <div class="mx-auto h-10 w-10 rounded-lg bg-zinc-800/70 grid place-items-center mb-4">
        <svg viewBox="0 0 24 24" class="h-5 w-5 text-zinc-400" fill="none" stroke="currentColor" stroke-width="1.6">
          <path d="M6 8a6 6 0 1 1 12 0c0 7 3 7 3 9H3c0-2 3-2 3-9Z" stroke-linejoin="round" />
          <path d="M10 19a2 2 0 0 0 4 0" />
        </svg>
      </div>
      <h2 class="text-base font-medium text-zinc-100">No alert rules yet</h2>
      <p class="mt-1 text-sm text-zinc-500 max-w-md mx-auto">
        Add a rule to watch a metric and notify you when it crosses a threshold.
      </p>
      {#if channels.length === 0}
        <p class="mt-3 text-xs text-zinc-500">
          Tip: add a <a class="text-emerald-400 hover:underline" href="/settings/channels">notification channel</a> first.
        </p>
      {/if}
    </div>
  {:else if rules.length > 0}
    <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Name</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Metric</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Condition</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Severity</th>
              <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Channels</th>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each rules as r (r.id)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-4 sm:px-5 py-2.5 whitespace-nowrap">
                  <div class="flex items-center gap-2">
                    <span class={r.enabled ? 'h-1.5 w-1.5 rounded-full bg-emerald-400' : 'h-1.5 w-1.5 rounded-full bg-zinc-600'}></span>
                    <span class="text-zinc-100 font-medium">{r.name}</span>
                  </div>
                </td>
                <td class="px-3 py-2.5 text-zinc-400 font-mono text-xs whitespace-nowrap">{r.metric}</td>
                <td class="px-3 py-2.5 text-zinc-400 numeric text-xs font-mono whitespace-nowrap">
                  {r.agg} {r.comparator} {r.threshold} for {r.for_s}s in {r.window_s}s
                </td>
                <td class="px-3 py-2.5 whitespace-nowrap">
                  <span class="text-xs px-2 py-0.5 rounded-full border {severityClass(r.severity, 'pill')}">
                    {r.severity}
                  </span>
                </td>
                <td class="px-3 py-2.5 text-zinc-400 text-xs numeric">{r.channel_ids.length}</td>
                <td class="px-4 sm:px-5 py-2.5 text-xs whitespace-nowrap">
                  <div class="flex items-center gap-1">
                    <button type="button" onclick={() => (editing = r)} class="px-2 py-1 rounded-md hover:bg-zinc-800/60 text-zinc-300">Edit</button>
                    <button type="button" onclick={() => toggleRule(r)} class="px-2 py-1 rounded-md hover:bg-zinc-800/60 text-zinc-300">
                      {r.enabled ? 'Disable' : 'Enable'}
                    </button>
                    <button type="button" onclick={() => (toDelete = r)} class="px-2 py-1 rounded-md hover:bg-rose-950/40 text-rose-300">Delete</button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}

  <section class="mt-8">
    <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
      <div class="flex flex-wrap items-center gap-3">
        <h2 class="text-sm uppercase tracking-wider text-zinc-500">Recent history</h2>
        {#if historyNotice}<span class="text-xs text-zinc-400">{historyNotice}</span>{/if}
      </div>
      <div class="flex items-center gap-1">
        <a
          href="/api/v1/alerts/history?format=csv"
          download
          class="inline-flex items-center px-2 py-1 rounded-md text-[10px] uppercase tracking-wider text-zinc-500 hover:text-zinc-200 hover:bg-zinc-800/60 transition-colors">
          Export CSV
        </a>
        <button
          type="button"
          disabled={history.length === 0}
          onclick={() => { clearOlderThanDays = 0; clearOpen = true; }}
          class="px-2 py-1 rounded-md text-xs text-rose-300 hover:bg-rose-950/40 disabled:opacity-40 disabled:cursor-not-allowed">
          Clear
        </button>
      </div>
    </div>
    <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      {#if history.length === 0}
        <div class="px-5 py-8 text-center text-zinc-500 text-sm">No alerts triggered yet</div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
              <tr>
                <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">When</th>
                <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Rule</th>
                <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Host</th>
                <th class="text-right font-medium px-3 py-2.5 whitespace-nowrap">Value</th>
                <th class="text-left font-medium px-3 py-2.5 whitespace-nowrap">Status</th>
                <th class="text-left font-medium px-4 sm:px-5 py-2.5 whitespace-nowrap">Labels</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each history as h (h.id)}
                <tr class="hover:bg-zinc-900/60">
                  <td class="px-4 sm:px-5 py-2 text-zinc-400 text-xs numeric whitespace-nowrap">{timeAgo(h.fired_at)}</td>
                  <td class="px-3 py-2 text-zinc-200 font-mono text-xs whitespace-nowrap">{h.rule_name}</td>
                  <td class="px-3 py-2 text-zinc-400 font-mono text-xs whitespace-nowrap">{h.hostname}</td>
                  <td class="px-3 py-2 text-right numeric text-zinc-300">{h.value.toFixed(2)}</td>
                  <td class="px-3 py-2 text-xs whitespace-nowrap">
                    {#if h.resolved_at}
                      <span class="text-emerald-400">resolved</span>
                    {:else}
                      <span class="text-rose-400">triggered</span>
                    {/if}
                  </td>
                  <td class="px-4 sm:px-5 py-2 text-zinc-500 text-xs font-mono whitespace-nowrap">
                    {Object.entries(h.labels ?? {}).map(([k, v]) => `${k}=${v}`).join(' ') || '—'}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  </section>
</div>

{#if creating}
  <AlertRuleEditor
    {channels}
    rule={null}
    onClose={() => (creating = false)}
    onSaved={() => {
      creating = false;
      refresh();
    }} />
{/if}

{#if editing}
  <AlertRuleEditor
    {channels}
    rule={editing}
    onClose={() => (editing = null)}
    onSaved={() => {
      editing = null;
      refresh();
    }} />
{/if}

<ConfirmDialog
  open={toDelete !== null}
  title="Delete alert rule"
  message={toDelete ? `Delete rule "${toDelete.name}"? Its history rows will be retained, but no new alerts will be triggered.` : ''}
  confirmLabel="Delete"
  danger
  onconfirm={doDelete}
  onclose={() => (toDelete = null)}
/>

<ConfirmDialog
  open={clearOpen}
  title="Clear alert history"
  body={clearHistoryBody}
  confirmLabel="Clear"
  danger
  onconfirm={clearHistory}
  onclose={() => (clearOpen = false)}
/>

{#snippet clearHistoryBody()}
  <p class="text-sm text-zinc-300">Delete resolved alert history. Still-firing alerts are kept.</p>
  <label for="clear-history-age" class="mt-3 block text-xs uppercase tracking-wider text-zinc-500">Entries to clear</label>
  <select
    id="clear-history-age"
    bind:value={clearOlderThanDays}
    class="mt-1.5 w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm text-zinc-200">
    <option value={0}>All resolved entries</option>
    <option value={7}>Older than 7 days</option>
    <option value={30}>Older than 30 days</option>
    <option value={90}>Older than 90 days</option>
  </select>
{/snippet}
