<script lang="ts">
  import { api, type AlertRule, type AlertRuleInput, type Channel, type Host, type MetricMeta } from '$lib/api';
  import { modalFocus } from '$lib/modal';

  let {
    rule,
    channels,
    onClose,
    onSaved
  }: {
    rule: AlertRule | null;
    channels: Channel[];
    onClose: () => void;
    onSaved: () => void;
  } = $props();

  let metrics = $state<MetricMeta[]>([]);
  let hosts = $state<Host[]>([]);
  let busy = $state(false);
  let error = $state<string | null>(null);

  let name = $state('');
  let metric = $state('cpu_total_pct');
  let comparator = $state('>');
  let threshold = $state(80);
  let windowS = $state(60);
  let forS = $state(120);
  let agg = $state('avg');
  let severity = $state('warning');
  let cooldownS = $state(600);
  let channelIds = $state<number[]>([]);
  const shortSmartWindow = $derived((metric.startsWith('smart_') || metric.startsWith('raid_')) && windowS < 900);

  type ScopeMode = 'all' | 'ids' | 'tags';
  let scopeMode = $state<ScopeMode>('all');
  let scopeIds = $state<number[]>([]);
  let scopeTagsText = $state('');
  let labelText = $state('');

  type RulePreset = {
    label: string;
    name: string;
    metric: string;
    comparator: string;
    threshold: number;
    severity: string;
    agg?: string;
    windowS?: number;
  };

  const suggestedRules: RulePreset[] = [
    { label: 'Backup stale', name: 'Backup stale', metric: 'backup_last_success_age_s', comparator: '>', threshold: 93600, severity: 'warning' },
    { label: 'Backup failed', name: 'Backup failed', metric: 'backup_last_run_ok', comparator: '<', threshold: 1, severity: 'critical' },
    { label: 'Check failing', name: 'Backup check failing', metric: 'backup_check_ok', comparator: '<', threshold: 1, severity: 'warning' },
    { label: 'Check overdue', name: 'Backup check overdue', metric: 'backup_check_age_s', comparator: '>', threshold: 3456000, severity: 'warning' },
    { label: 'Backup agent outdated', name: 'Backup agent outdated', metric: 'backup_agent_stale', comparator: '>', threshold: 0, severity: 'warning', agg: 'last', windowS: 120 }
  ];

  function pairsToText(pairs: Record<string, string> | undefined | null): string {
    if (!pairs) return '';
    return Object.entries(pairs)
      .map(([k, v]) => `${k}=${v}`)
      .join('\n');
  }

  function parsePairs(text: string): Record<string, string> {
    const out: Record<string, string> = {};
    for (const raw of text.split('\n')) {
      const line = raw.trim();
      if (!line) continue;
      const i = line.indexOf('=');
      if (i <= 0) continue;
      const k = line.slice(0, i).trim();
      const v = line.slice(i + 1).trim();
      if (k) out[k] = v;
    }
    return out;
  }

  function loadScope(sel: unknown) {
    if (sel && typeof sel === 'object') {
      const s = sel as { all?: boolean; ids?: number[]; tags?: Record<string, string> };
      if (s.ids && s.ids.length) {
        scopeMode = 'ids';
        scopeIds = [...s.ids];
        scopeTagsText = '';
        return;
      }
      if (s.tags && Object.keys(s.tags).length) {
        scopeMode = 'tags';
        scopeIds = [];
        scopeTagsText = pairsToText(s.tags);
        return;
      }
    }
    scopeMode = 'all';
    scopeIds = [];
    scopeTagsText = '';
  }

  function buildScope(): unknown {
    if (scopeMode === 'ids' && scopeIds.length > 0) return { ids: scopeIds };
    if (scopeMode === 'tags') {
      const tags = parsePairs(scopeTagsText);
      if (Object.keys(tags).length > 0) return { tags };
    }
    return { all: true };
  }

  $effect(() => {
    name = rule?.name ?? '';
    metric = rule?.metric ?? 'cpu_total_pct';
    comparator = rule?.comparator ?? '>';
    threshold = rule?.threshold ?? 80;
    windowS = rule?.window_s ?? 60;
    forS = rule?.for_s ?? 120;
    agg = rule?.agg ?? 'avg';
    severity = rule?.severity ?? 'warning';
    cooldownS = rule?.cooldown_s ?? 600;
    channelIds = rule?.channel_ids ?? [];
    loadScope(rule?.host_selector);
    labelText = pairsToText(rule?.label_selector);
  });

  $effect(() => {
    api.metrics().then((m) => {
      metrics = m.sort((a, b) => a.name.localeCompare(b.name));
    });
    api.hosts().then((h) => {
      hosts = h.sort((a, b) => a.hostname.localeCompare(b.hostname));
    });
  });

  async function save(e: Event) {
    e.preventDefault();
    if (!name.trim()) {
      error = 'Name is required';
      return;
    }
    if (scopeMode === 'ids' && scopeIds.length === 0) {
      error = 'Select at least one host, or switch the scope to All hosts';
      return;
    }
    if (scopeMode === 'tags' && Object.keys(parsePairs(scopeTagsText)).length === 0) {
      error = 'Enter at least one key=value tag, or switch the scope to All hosts';
      return;
    }
    busy = true;
    error = null;
    const payload: AlertRuleInput = {
      name,
      host_selector: buildScope(),
      metric,
      label_selector: parsePairs(labelText),
      comparator,
      threshold,
      window_s: windowS,
      for_s: forS,
      agg,
      severity,
      cooldown_s: cooldownS,
      channel_ids: channelIds,
      enabled: rule?.enabled ?? true
    };
    try {
      if (rule) await api.alertUpdate(rule.id, payload);
      else await api.alertCreate(payload);
      onSaved();
    } catch (err) {
      error = (err as Error).message;
    } finally {
      busy = false;
    }
  }

  function toggleChannel(id: number) {
    channelIds = channelIds.includes(id) ? channelIds.filter((x) => x !== id) : [...channelIds, id];
  }

  function toggleHost(id: number) {
    scopeIds = scopeIds.includes(id) ? scopeIds.filter((x) => x !== id) : [...scopeIds, id];
  }

  function applyPreset(preset: RulePreset) {
    name = preset.name;
    metric = preset.metric;
    comparator = preset.comparator;
    threshold = preset.threshold;
    severity = preset.severity;
    agg = preset.agg ?? 'last';
    windowS = preset.windowS ?? 60;
    forS = 60;
    cooldownS = 600;
    labelText = '';
  }
</script>

<div role="dialog" aria-modal="true" tabindex="-1" use:modalFocus class="fixed inset-0 z-30 bg-zinc-950/60 backdrop-blur-sm flex items-end sm:items-center justify-center p-0 sm:p-4" onclick={(e) => { if (e.target === e.currentTarget) onClose(); }} onkeydown={(e) => { if (e.key === 'Escape') onClose(); }}>
  <div class="w-full max-w-2xl rounded-t-xl sm:rounded-xl border border-zinc-800 bg-zinc-900 shadow-2xl overflow-hidden flex flex-col max-h-[90vh] sm:max-h-[calc(100vh-2rem)]">
    <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex items-center justify-between shrink-0">
      <h2 class="text-base font-medium text-zinc-100">{rule ? 'Edit alert rule' : 'New alert rule'}</h2>
      <button type="button" onclick={onClose} class="text-zinc-500 hover:text-zinc-200 text-sm">Cancel</button>
    </header>

    <form onsubmit={save} class="p-4 sm:p-5 space-y-4 overflow-y-auto">
      <div>
        <div class="text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Suggested rules</div>
        <div class="flex flex-wrap gap-2">
          {#each suggestedRules as preset (preset.metric)}
            <button
              type="button"
              onclick={() => applyPreset(preset)}
              class="rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1 text-xs text-zinc-300 hover:border-zinc-700 hover:text-zinc-100">
              {preset.label}
            </button>
          {/each}
        </div>
      </div>

      <div>
        <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rname">Name</label>
        <input id="rname" bind:value={name} required class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm" />
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rmetric">Metric</label>
          <select id="rmetric" bind:value={metric} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono">
            {#each metrics as m (m.id)}
              <option value={m.name}>{m.name}{m.unit ? ` (${m.unit})` : ''}</option>
            {/each}
          </select>
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="ragg">Aggregate over window</label>
          <select id="ragg" bind:value={agg} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
            <option value="avg">average</option>
            <option value="max">max</option>
            <option value="min">min</option>
            <option value="last">last</option>
          </select>
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rcmp">Comparator</label>
          <select id="rcmp" bind:value={comparator} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
            <option value=">">&gt;</option>
            <option value=">=">&ge;</option>
            <option value="<">&lt;</option>
            <option value="<=">&le;</option>
            <option value="==">=</option>
            <option value="!=">≠</option>
          </select>
        </div>
        <div class="col-span-2">
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rth">Threshold</label>
          <input id="rth" type="number" step="any" bind:value={threshold} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rwin">Window (s)</label>
          <input id="rwin" type="number" min="10" bind:value={windowS} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rfor">For (s)</label>
          <input id="rfor" type="number" min="0" bind:value={forS} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rcd">Cooldown (s)</label>
          <input id="rcd" type="number" min="0" bind:value={cooldownS} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
        </div>
      </div>

      {#if shortSmartWindow}
        <div class="rounded-md border border-amber-900/50 bg-amber-950/20 px-3 py-2 text-xs text-amber-200/90 numeric">SMART and RAID metrics are sampled about every 5 minutes on current agents. Use a window of at least 15 minutes to avoid alert flapping.</div>
      {/if}

      <div>
        <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rsev">Severity</label>
        <select id="rsev" bind:value={severity} class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
          <option value="info">info</option>
          <option value="warning">warning</option>
          <option value="critical">critical</option>
        </select>
      </div>

      <div>
        <div class="text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Scope</div>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5">
          <label class="flex items-center gap-2 text-sm text-zinc-300">
            <input type="radio" name="scope" value="all" checked={scopeMode === 'all'} onchange={() => (scopeMode = 'all')} class="accent-emerald-500" />
            All hosts
          </label>
          <label class="flex items-center gap-2 text-sm text-zinc-300">
            <input type="radio" name="scope" value="ids" checked={scopeMode === 'ids'} onchange={() => (scopeMode = 'ids')} class="accent-emerald-500" />
            Specific hosts
          </label>
          <label class="flex items-center gap-2 text-sm text-zinc-300">
            <input type="radio" name="scope" value="tags" checked={scopeMode === 'tags'} onchange={() => (scopeMode = 'tags')} class="accent-emerald-500" />
            By tags
          </label>
        </div>
        {#if scopeMode === 'ids'}
          <div class="mt-2 max-h-40 overflow-y-auto rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 space-y-1">
            {#if hosts.length === 0}
              <div class="text-xs text-zinc-500 italic">No hosts registered yet.</div>
            {:else}
              {#each hosts as h (h.id)}
                <label class="flex items-center gap-2 text-sm text-zinc-300">
                  <input type="checkbox" checked={scopeIds.includes(h.id)} onchange={() => toggleHost(h.id)} class="accent-emerald-500" />
                  <span class="truncate">{h.hostname}</span>
                  <span class="text-[10px] uppercase text-zinc-500">{h.os || ''}</span>
                </label>
              {/each}
            {/if}
          </div>
        {:else if scopeMode === 'tags'}
          <textarea
            bind:value={scopeTagsText}
            rows="3"
            placeholder={'env=prod\nrole=db'}
            class="mt-2 w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono"
          ></textarea>
          <p class="mt-1 text-[11px] text-zinc-500">One <code class="text-zinc-400">key=value</code> per line. Hosts match when they carry every tag.</p>
        {/if}
      </div>

      <div>
        <label class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5" for="rlbl">Label filter</label>
        <textarea
          id="rlbl"
          bind:value={labelText}
          rows="2"
          placeholder={'mount=/\nfstype=ext4'}
          class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono"
        ></textarea>
        <p class="mt-1 text-[11px] text-zinc-500">Restrict to metric series matching every <code class="text-zinc-400">key=value</code>. Blank for all series.</p>
      </div>

      <div>
        <div class="text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Channels</div>
        {#if channels.length === 0}
          <div class="text-xs text-zinc-500 italic">
            No channels configured. <a class="text-emerald-400 hover:underline" href="/settings/channels">Add one</a> to deliver alerts.
          </div>
        {:else}
          <div class="space-y-1.5">
            {#each channels as c (c.id)}
              <label class="flex items-center gap-2 text-sm text-zinc-300">
                <input type="checkbox" checked={channelIds.includes(c.id)} onchange={() => toggleChannel(c.id)} class="accent-emerald-500" />
                <span class="font-mono text-xs">{c.name}</span>
                <span class="text-[10px] uppercase text-zinc-500">{c.kind}</span>
              </label>
            {/each}
          </div>
        {/if}
      </div>

      {#if error}
        <div class="rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
      {/if}

      <div class="flex items-center justify-end gap-2 pt-2">
        <button type="button" onclick={onClose} class="text-sm px-3 py-2 text-zinc-400 hover:text-zinc-200">Cancel</button>
        <button type="submit" disabled={busy} class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50">
          {busy ? 'Saving…' : rule ? 'Save changes' : 'Create rule'}
        </button>
      </div>
    </form>
  </div>
</div>
