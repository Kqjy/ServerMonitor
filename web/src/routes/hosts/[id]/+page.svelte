<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import { api, type Host, type ActiveAlert } from '$lib/api';
  import { bytes, statusFor, timeAgo, severityClass, severityRank } from '$lib/format';
  import { subscribeAlerts } from '$lib/sse';
  import { rangeLabel, loadRange, saveRange, writeRangeToUrl, rangeEquals, type Range } from '$lib/time';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import EditHostDialog from '$lib/components/EditHostDialog.svelte';
  import ReconfigureDialog from '$lib/components/ReconfigureDialog.svelte';
  import CustomRangePicker from '$lib/components/CustomRangePicker.svelte';
  import OverviewTab from '$lib/components/host/OverviewTab.svelte';
  import MemoryTab from '$lib/components/host/MemoryTab.svelte';
  import DiskTab from '$lib/components/host/DiskTab.svelte';
  import NetworkTab from '$lib/components/host/NetworkTab.svelte';
  import ProcessesTab from '$lib/components/host/ProcessesTab.svelte';
  import ContainersTab from '$lib/components/host/ContainersTab.svelte';
  import PortsTab from '$lib/components/host/PortsTab.svelte';
  import SensorsTab from '$lib/components/host/SensorsTab.svelte';
  import GpuTab from '$lib/components/host/GpuTab.svelte';
  import BackupsTab from '$lib/components/host/BackupsTab.svelte';

  const id = $derived(Number($page.params.id));
  const tab = $derived(($page.url.searchParams.get('tab') ?? 'overview') as TabName);
  type TabName = 'overview' | 'memory' | 'disk' | 'network' | 'processes' | 'containers' | 'ports' | 'sensors' | 'gpu' | 'backups';
  const showRange = $derived(tab !== 'processes' && tab !== 'containers' && tab !== 'ports');

  let host = $state<Host | null>(null);
  let activeAlerts = $state<ActiveAlert[]>([]);
  let error = $state<string | null>(null);
  let range = $state<Range>(loadRange($page.url.searchParams));
  let timer: ReturnType<typeof setInterval> | null = null;
  let editing = $state(false);
  let reconfiguring = $state(false);
  let pickerOpen = $state(false);
  let upgradeBusy = $state(false);
  let upgradeError = $state<string | null>(null);
  let installBaseUrl = $state(typeof window !== 'undefined' ? window.location.origin : '');
  let agentHealthCopy = $state<'idle' | 'stale' | 'perms' | 'failed'>('idle');
  let agentCpuPct = $state<number | null>(null);
  let agentRssBytes = $state<number | null>(null);
  let agentHealthCopyTimer: ReturnType<typeof setTimeout> | null = null;
  const backupAgentState = $derived(host?.collector_status?.backup?.state ?? '');
  const backupAgentMessage = $derived(host?.collector_status?.backup?.message ?? '');
  const staleAgentInstallCommand = $derived((host?.os ?? '').toLowerCase().startsWith('windows')
    ? `iex (iwr -useb ${installBaseUrl}/install.ps1).Content`
    : `sudo bash -c "curl -fsSL ${installBaseUrl}/install.sh | bash"`);
  const agentPermsCommand = 'sudo chown root:root /usr/local/bin/sm-agent && sudo chmod 0755 /usr/local/bin/sm-agent';

  const tabs: { value: TabName; label: string }[] = [
    { value: 'overview', label: 'Overview' },
    { value: 'memory', label: 'Memory' },
    { value: 'disk', label: 'Disk' },
    { value: 'network', label: 'Network' },
    { value: 'processes', label: 'Processes' },
    { value: 'containers', label: 'Containers' },
    { value: 'ports', label: 'Ports' },
    { value: 'sensors', label: 'Sensors' },
    { value: 'gpu', label: 'GPU' },
    { value: 'backups', label: 'Backups' }
  ];

  async function refresh() {
    try {
      const [h, alerts, cpu, rss] = await Promise.all([
        api.host(id),
        api.hostActiveAlerts(id).catch(() => []),
        api.series({ host: id, metric: 'agent_cpu_pct', from: '-2m', step: 10 }).catch(() => null),
        api.series({ host: id, metric: 'agent_rss_bytes', from: '-2m', step: 10 }).catch(() => null)
      ]);
      host = h;
      activeAlerts = alerts;
      agentCpuPct = cpu?.points.at(-1)?.v ?? null;
      agentRssBytes = rss?.points.at(-1)?.v ?? null;
      error = null;
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function requestUpgrade() {
    if (!host || upgradeBusy) return;
    upgradeBusy = true;
    upgradeError = null;
    try {
      const updated = await api.requestHostUpgrade(host.id);
      host = updated;
    } catch (e) {
      upgradeError = (e as Error).message;
    } finally {
      upgradeBusy = false;
    }
  }

  async function loadInstallBaseUrl() {
    try {
      const info = await api.serverInfo();
      installBaseUrl = (info.url || window.location.origin).replace(/\/+$/, '');
    } catch {
      installBaseUrl = window.location.origin;
    }
  }

  async function copyAgentHealthCommand(command: string, key: 'stale' | 'perms') {
    if (agentHealthCopyTimer) clearTimeout(agentHealthCopyTimer);
    try {
      await navigator.clipboard.writeText(command);
      agentHealthCopy = key;
    } catch {
      agentHealthCopy = 'failed';
    }
    agentHealthCopyTimer = setTimeout(() => (agentHealthCopy = 'idle'), 1500);
  }

  $effect(() => {
    if (id) refresh();
  });

  let prevRange: Range | null = null;
  $effect(() => {
    const r = range;
    if (prevRange === null || !rangeEquals(prevRange, r)) {
      saveRange(r);
      writeRangeToUrl(r);
      prevRange = r;
    }
  });

  function setTab(t: TabName) {
    const u = new URL(window.location.href);
    u.searchParams.set('tab', t);
    history.replaceState(history.state, '', u.toString());
    window.dispatchEvent(new PopStateEvent('popstate'));
  }

  let tabModel = $state<TabName>('overview');
  $effect(() => {
    tabModel = tab;
  });
  $effect(() => {
    if (tabModel !== tab) setTab(tabModel);
  });

  const topSeverity = $derived(
    activeAlerts.reduce<string>((acc, a) => ((severityRank[a.severity] ?? 0) > (severityRank[acc] ?? 0) ? a.severity : acc), '')
  );

  $effect(() => {
    if (!id) return;
    const unsub = subscribeAlerts(() => refresh(), id);
    return unsub;
  });

  onMount(() => {
    void loadInstallBaseUrl();
    timer = setInterval(refresh, 15_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (agentHealthCopyTimer) clearTimeout(agentHealthCopyTimer);
  });
</script>

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-4 sm:py-6">
  <div class="mb-2">
    <a href="/" class="text-xs text-zinc-500 hover:text-zinc-300 transition-colors">← All hosts</a>
  </div>

  {#if error}
    <div class="mb-4 rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">{error}</div>
  {/if}
  {#if !host}
    {#if !error}
      <div class="h-10 w-full max-w-xs rounded shimmer"></div>
      <div class="mt-6 h-64 rounded shimmer"></div>
    {/if}
  {:else}
    {@const s = statusFor(host.last_seen, host.sample_interval_s || 10)}
    {#if activeAlerts.length > 0}
      <div class="mb-4 rounded-lg border px-4 py-3 {severityClass(topSeverity, 'banner')}">
        <div class="flex items-center gap-2 text-sm font-medium">
          <span class="h-2 w-2 rounded-full bg-current"></span>
          {activeAlerts.length} alert{activeAlerts.length === 1 ? '' : 's'} triggered on this host
        </div>
        <ul class="mt-2 space-y-1 text-xs">
          {#each activeAlerts as a (a.rule_id + ':' + (a.label_key ?? ''))}
            <li class="flex flex-wrap items-baseline gap-x-3 numeric tabular-nums">
              <a href="/alerts" class="font-medium {severityClass(a.severity, 'row')} hover:underline">{a.rule_name}</a>
              <span class="text-zinc-400">{a.metric}{a.value !== undefined ? ` = ${a.value.toFixed(2)}` : ''}</span>
              {#if a.label_key}<span class="text-zinc-500">{a.label_key}</span>{/if}
              <span class="text-zinc-500">since {timeAgo(a.since)}</span>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
    <div class="flex flex-wrap items-end gap-x-6 gap-y-3 mb-4">
      <div class="min-w-0 flex-1">
        <h1 class="flex items-baseline gap-3 text-xl sm:text-2xl font-semibold tracking-tight">
          <StatusDot status={s} size="lg" />
          <span class="truncate">{host.hostname}</span>
        </h1>
        <div class="mt-1 text-[11px] sm:text-xs text-zinc-500 numeric break-words">
          {host.os || '—'}{host.arch ? ` · ${host.arch}` : ''}{host.kernel ? ` · ${host.kernel}` : ''}
          · agent v{host.agent_version || '?'} · seen {timeAgo(host.last_seen)}
          {#if agentCpuPct !== null && agentRssBytes !== null}<span> · {agentCpuPct.toFixed(1)}% CPU · {bytes(agentRssBytes)}</span>{/if}
        </div>
        {#if host.update_available || backupAgentState === 'stale_agent' || backupAgentState === 'agent_perms'}
          <div class="mt-3 rounded-lg border px-4 py-3 {backupAgentState === 'agent_perms' && !host.update_available ? 'border-rose-900/50 bg-rose-950/20' : 'border-amber-900/50 bg-amber-950/20'}">
            <div class="space-y-3">
            {#if host.update_available}
              <div class="flex flex-wrap items-center gap-2 text-xs">
            <span class="text-sky-300">Update to v{host.latest_agent_version} available</span>
            {#if host.externally_managed}
              <span
                class="text-zinc-400"
                title="This agent runs from a container image (or a read-only filesystem) and cannot replace its own binary. Rebuild the agent image, bump SM_AGENT_IMAGE, and redeploy to update."
              >
                Managed externally — redeploy a new agent image to update
              </span>
            {:else if host.upgrade_stalled}
              <span
                class="text-amber-400"
                title="The agent kept failing to replace its own binary — typically a read-only filesystem or a containerized deploy. Auto-update is paused. If this host runs the agent from a container image, redeploy a new image tag; otherwise re-run the install script or check the agent logs."
              >
                Self-update failing — still on v{host.agent_version || '?'}; redeploy or re-install to update
              </span>
              {#if host.supports_remote_upgrade}
                {#if host.upgrade_pending}
                  <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md text-sky-200 bg-sky-500/10 border border-sky-500/30">
                    <span class="h-1.5 w-1.5 rounded-full bg-sky-300 animate-pulse"></span>
                    Pending next check-in
                  </span>
                {:else}
                  <button
                    type="button"
                    onclick={requestUpgrade}
                    disabled={upgradeBusy}
                    class="px-2.5 py-0.5 rounded-md bg-amber-500/15 border border-amber-500/40 text-amber-200 hover:bg-amber-500/25 disabled:opacity-50"
                  >
                    {upgradeBusy ? 'Sending…' : 'Retry update'}
                  </button>
                {/if}
              {/if}
            {:else if host.supports_remote_upgrade}
              {#if host.upgrading}
                <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md text-sky-200 bg-sky-500/10 border border-sky-500/30">
                  <span class="h-1.5 w-1.5 rounded-full bg-sky-300 animate-pulse"></span>
                  Updating…
                </span>
              {:else if host.upgrade_pending}
                <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md text-sky-200 bg-sky-500/10 border border-sky-500/30">
                  <span class="h-1.5 w-1.5 rounded-full bg-sky-300 animate-pulse"></span>
                  Pending next check-in
                </span>
              {:else}
                <button
                  type="button"
                  onclick={requestUpgrade}
                  disabled={upgradeBusy}
                  class="px-2.5 py-0.5 rounded-md bg-sky-500/15 border border-sky-500/40 text-sky-200 hover:bg-sky-500/25 disabled:opacity-50"
                >
                  {upgradeBusy ? 'Sending…' : 'Update now'}
                </button>
              {/if}
              {#if host.auto_upgrade}
                <span class="text-zinc-500">auto-update on</span>
              {:else}
                <span class="text-zinc-500">auto-update off</span>
              {/if}
            {:else}
              <span
                class="text-amber-400"
                title="Agent versions older than 0.1.1 cannot self-upgrade. Re-run the install script on this host."
              >
                Manual upgrade required (agent &lt; v0.1.1)
              </span>
            {/if}
            {#if upgradeError}
              <span class="text-rose-300">{upgradeError}</span>
            {/if}
              </div>
            {/if}
            {#if backupAgentState === 'stale_agent'}
              <div class="text-xs text-amber-100/90 {host.update_available ? 'border-t border-amber-900/40 pt-3' : ''}">
                <div>{backupAgentMessage}</div>
                <div class="mt-1 text-amber-100/70">Re-run the install script to refresh the privileged backup agent copy.</div>
                <div class="mt-2 flex items-center gap-2">
                  <code class="min-w-0 flex-1 overflow-x-auto rounded-md bg-zinc-950/70 px-2.5 py-1.5 text-zinc-200 select-text">{staleAgentInstallCommand}</code>
                  <button type="button" onclick={() => copyAgentHealthCommand(staleAgentInstallCommand, 'stale')} class="shrink-0 text-[11px] px-2 py-1 rounded bg-zinc-800 hover:bg-zinc-700 {agentHealthCopy === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
                    {agentHealthCopy === 'stale' ? 'copied' : agentHealthCopy === 'failed' ? 'copy failed' : 'copy'}
                  </button>
                </div>
              </div>
            {/if}
            {#if backupAgentState === 'agent_perms'}
              <div class="text-xs text-rose-100/90 {host.update_available ? 'rounded-md border border-rose-900/40 bg-rose-950/30 p-3' : ''}">
                <div>{backupAgentMessage}</div>
                <div class="mt-2 flex items-center gap-2">
                  <code class="min-w-0 flex-1 overflow-x-auto rounded-md bg-zinc-950/70 px-2.5 py-1.5 text-zinc-200 select-text">{agentPermsCommand}</code>
                  <button type="button" onclick={() => copyAgentHealthCommand(agentPermsCommand, 'perms')} class="shrink-0 text-[11px] px-2 py-1 rounded bg-zinc-800 hover:bg-zinc-700 {agentHealthCopy === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
                    {agentHealthCopy === 'perms' ? 'copied' : agentHealthCopy === 'failed' ? 'copy failed' : 'copy'}
                  </button>
                </div>
                <div class="mt-2 text-rose-100/70">Upgrading the agent makes the privileged sync service repair this automatically going forward. Re-running the installer also fixes it.</div>
              </div>
            {/if}
            </div>
          </div>
        {/if}
      </div>
      <div class="w-full sm:w-auto sm:ml-auto flex flex-wrap items-center gap-2 sm:gap-3 text-xs">
        {#if showRange}
          <div class="relative">
            <button
              type="button"
              onclick={() => (pickerOpen = !pickerOpen)}
              aria-haspopup="dialog"
              aria-expanded={pickerOpen}
              title="Select time range"
              class="inline-flex items-center gap-1.5 px-2 sm:px-2.5 py-1 rounded-md border border-zinc-800 transition-colors numeric text-zinc-300 hover:text-zinc-100 hover:bg-zinc-800/40">
              <svg viewBox="0 0 24 24" class="h-3.5 w-3.5 text-zinc-500" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                <circle cx="12" cy="12" r="9" />
                <path d="M12 7.5V12l3 1.5" />
              </svg>
              <span>{rangeLabel(range)}</span>
              <svg viewBox="0 0 24 24" class="h-3 w-3 text-zinc-500" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M6 9l6 6 6-6" />
              </svg>
            </button>
            {#if pickerOpen}
              <CustomRangePicker
                value={range}
                onApply={(r) => { range = r; pickerOpen = false; }}
                onCancel={() => (pickerOpen = false)} />
            {/if}
          </div>
          <span class="h-4 w-px bg-zinc-800 shrink-0"></span>
        {/if}
        <button
          type="button"
          aria-label="Reconfigure agent"
          title="Reconfigure agent capabilities"
          onclick={() => (reconfiguring = true)}
          class="inline-flex items-center gap-1.5 px-2 sm:px-2.5 py-1 rounded-md text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/40 transition-colors shrink-0"
        >
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6" />
          </svg>
          <span class="hidden sm:inline">Reconfigure</span>
        </button>
        <button
          type="button"
          aria-label="Edit host"
          title="Edit hostname and interval"
          onclick={() => (editing = true)}
          class="inline-flex items-center gap-1.5 px-2 sm:px-2.5 py-1 rounded-md text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/40 transition-colors shrink-0"
        >
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <path d="M12 20h9" />
            <path d="M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4 12.5-12.5Z" />
          </svg>
          <span class="hidden sm:inline">Edit</span>
        </button>
      </div>
    </div>

    <Tabs {tabs} bind:value={tabModel} />

    <div class="mt-6">
      {#if tabModel === 'overview'}<OverviewTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'memory'}<MemoryTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'disk'}<DiskTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} enabledCollectors={host.enabled_collectors ?? []} collectorStatus={host.collector_status ?? {}} />
      {:else if tabModel === 'network'}<NetworkTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'processes'}<ProcessesTab hostId={id} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'containers'}<ContainersTab hostId={id} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'ports'}<PortsTab hostId={id} sampleIntervalS={host.sample_interval_s} collectorStatus={host.collector_status ?? {}} />
      {:else if tabModel === 'sensors'}<SensorsTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'gpu'}<GpuTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} />
      {:else if tabModel === 'backups'}<BackupsTab hostId={id} {range} sampleIntervalS={host.sample_interval_s} collectorStatus={host.collector_status ?? {}} os={host.os} externallyManaged={host.externally_managed ?? false} />
      {/if}
    </div>

    {#if editing}
      <EditHostDialog
        host={host}
        onclose={() => (editing = false)}
        onsaved={async () => { editing = false; await refresh(); }}
      />
    {/if}

    {#if reconfiguring}
      <ReconfigureDialog host={host} onclose={() => (reconfiguring = false)} />
    {/if}
  {/if}
</div>
