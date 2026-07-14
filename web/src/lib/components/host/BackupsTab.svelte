<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import {
    api,
    type SeriesEntry,
    type CollectorStatus,
    type BackupRepoRow,
    type BackupSnapshot,
    type BackupTargetsResp,
    type BackupNode
  } from '$lib/api';
  import { rangeBoundsMs, rangeToFrom, rangeToTo, rangeEquals, chooseStepSec, type Range } from '$lib/time';
  import { bytes, timeAgo, timeUntil } from '$lib/format';
  import { TableSort } from '$lib/sort.svelte';
  import MultiChart, { type Series, type ChartZoom } from '$lib/components/MultiChart.svelte';
  import DownloadCsv from '$lib/components/DownloadCsv.svelte';
  import { subscribeHost, type LivePoint } from '$lib/sse';

  let {
    hostId,
    range,
    sampleIntervalS = 10,
    collectorStatus = {},
    os,
    externallyManaged = false
  }: {
    hostId: number;
    range: Range;
    sampleIntervalS?: number;
    collectorStatus?: Record<string, CollectorStatus>;
    os?: string;
    externallyManaged?: boolean;
  } = $props();

  const backupStatus = $derived(collectorStatus.backup);
  const isWindows = $derived((os ?? '').toLowerCase().startsWith('windows'));
  let baseUrl = $state(typeof window !== 'undefined' ? window.location.origin : '');

  async function loadServerInfo() {
    try {
      const info = await api.serverInfo();
      if (info?.url) baseUrl = info.url.replace(/\/+$/, '');
    } catch {}
  }

  let endpoint = $state<BackupTargetsResp | null>(null);
  const endpointConfigured = $derived(endpoint?.configured ?? false);
  const linkedRepo = $derived(endpoint?.targets.find((t) => t.host_id === hostId && !t.revoked_at)?.name ?? null);

  async function loadEndpoint() {
    try {
      endpoint = await api.backupTargets();
    } catch {}
  }

  let nodeRole = $state<BackupNode | null>(null);
  const isStorageNode = $derived(nodeRole !== null);

  async function loadNodeRole() {
    try {
      const resp = await api.backupNodes();
      nodeRole = resp.nodes.find((n) => n.host_id === hostId) ?? null;
    } catch {}
  }

  const enableSnippet = $derived(
    isWindows
      ? `$env:SM_ENABLE_BACKUP="1"; $env:SM_BACKUP_REPOS="<rest/s3 url>"; iex (iwr -useb ${baseUrl}/install.ps1).Content`
      : `SM_ENABLE_BACKUP=1 SM_BACKUP_REPOS="<rest/s3 url>" \\\n  sudo --preserve-env=SM_ENABLE_BACKUP,SM_BACKUP_REPOS bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`
  );

  type Tone = 'good' | 'warn' | 'bad' | 'none';

  type RepoView = {
    repo: string;
    engine?: string;
    tunnel: boolean;
    success: boolean;
    pending: boolean;
    error?: string;
    lastSuccessIso?: string;
    lastFinishedIso?: string;
    nextRunIso?: string;
    lastSuccessAgeS: number | null;
    durationS: number | null;
    addedBytes: number | null;
    totalBytes: number | null;
    snapshotCount: number | null;
    checkLastIso?: string;
    checkSuccess?: boolean;
    checkAgeS: number | null;
    backupTone: Tone;
    checkTone: Tone;
    snapshots: BackupSnapshot[];
  };

  let repos = $state<BackupRepoRow[]>([]);
  let backupsLoading = $state(true);
  let backupsError = $state<string | null>(null);
  let backupsBusy = $state(false);
  let backupsGen = 0;
  let backupsAC: AbortController | null = null;

  let addedHistory = $state<SeriesEntry[]>([]);
  let durationHistory = $state<SeriesEntry[]>([]);
  let fromMs = $state(0);
  let toMs = $state(0);
  let chartZoom = $state<ChartZoom>(null);
  let loadedStep = 0;
  let zoomFetched = false;
  let chartsLoading = $state(true);
  let masking = $state(false);
  let chartsError = $state<string | null>(null);
  let chartsGen = 0;
  let chartsAC: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let liveTimer: ReturnType<typeof setInterval> | null = null;
  let liveUnsub: (() => void) | null = null;
  let prevRange: Range | null = null;

  type LiveBackup = {
    running: boolean;
    elapsedS: number;
    lastSeen: number;
    percent: number;
    bytesDone: number;
    totalBytes: number;
  };

  let liveBackups = $state<Record<string, LiveBackup>>({});
  let liveNow = $state(Date.now());
  const liveFreshMs = 30_000;

  function isRunning(repo: string): boolean {
    const live = liveBackups[repo];
    return !!live && live.running && liveNow - live.lastSeen < liveFreshMs;
  }

  function displayedElapsed(repo: string): number {
    const live = liveBackups[repo];
    if (!live) return 0;
    return Math.max(0, live.elapsedS + (liveNow - live.lastSeen) / 1000);
  }

  function refreshAfterRun() {
    void loadBackups();
    void loadCharts();
  }

  function receiveLive(points: LivePoint[]) {
    const next = { ...liveBackups };
    const now = Date.now();
    let completed = false;
    for (const point of points) {
      const repo = point.labels?.repo;
      if (!repo) continue;
      const current = next[repo] ?? { running: false, elapsedS: 0, lastSeen: 0, percent: 0, bytesDone: 0, totalBytes: 0 };
      const previousRunning = current.running && now - current.lastSeen < liveFreshMs;
      const updated = { ...current };
      if (point.metric === 'backup_running') {
        updated.running = point.v === 1;
        updated.lastSeen = now;
        if (previousRunning && !updated.running) completed = true;
      } else if (point.metric === 'backup_run_elapsed_s') {
        updated.elapsedS = point.v;
      } else if (point.metric === 'backup_progress_pct') {
        updated.percent = Math.max(0, Math.min(100, point.v));
      } else if (point.metric === 'backup_progress_bytes') {
        updated.bytesDone = Math.max(0, point.v);
      } else if (point.metric === 'backup_progress_total_bytes') {
        updated.totalBytes = Math.max(0, point.v);
      }
      next[repo] = updated;
    }
    liveBackups = next;
    liveNow = now;
    if (completed) refreshAfterRun();
  }

  const runningRepos = $derived(Object.keys(liveBackups).filter((repo) => isRunning(repo)));

  let selectedRepo = $state<string>('');
  let selectedSnap = $state<string | null>(null);
  let includePaths = $state<string[]>([]);
  const snapSort = new TableSort<'time' | 'id'>('time');
  let copyState = $state<'idle' | 'copied' | 'failed'>('idle');
  let copyTimer: ReturnType<typeof setTimeout> | null = null;

  function toSeries(entries: SeriesEntry[]): Series[] {
    return entries.map((e) => ({
      label: e.labels.repo ?? Object.values(e.labels).join(' '),
      points: e.points
    }));
  }

  function ageS(iso?: string): number | null {
    if (!iso) return null;
    const t = new Date(iso).getTime();
    if (isNaN(t)) return null;
    return Math.max(0, (Date.now() - t) / 1000);
  }

  function backupTone(success: boolean, ageSecs: number | null, pending = false): Tone {
    if (pending) return 'none';
    if (!success) return 'bad';
    if (ageSecs === null) return 'none';
    if (ageSecs > 50 * 3600) return 'bad';
    if (ageSecs > 26 * 3600) return 'warn';
    return 'good';
  }

  function checkTone(checkSuccess: boolean | undefined, iso: string | undefined, ageSecs: number | null): Tone {
    if (checkSuccess === false) return 'bad';
    if (!iso || ageSecs === null) return 'none';
    if (ageSecs > 15 * 86400) return 'bad';
    if (ageSecs > 8 * 86400) return 'warn';
    return 'good';
  }

  const toneText: Record<Tone, string> = {
    good: 'text-emerald-300',
    warn: 'text-amber-300',
    bad: 'text-rose-300',
    none: 'text-zinc-500'
  };
  const toneDot: Record<Tone, string> = {
    good: 'bg-emerald-400',
    warn: 'bg-amber-400',
    bad: 'bg-rose-400',
    none: 'bg-zinc-600'
  };
  const tonePill: Record<Tone, string> = {
    good: 'border-emerald-900/60 bg-emerald-950/40 text-emerald-300',
    warn: 'border-amber-900/60 bg-amber-950/40 text-amber-300',
    bad: 'border-rose-900/60 bg-rose-950/40 text-rose-300',
    none: 'border-zinc-800 bg-zinc-950 text-zinc-500'
  };

  const views = $derived.by<RepoView[]>(() =>
    repos
      .map((r) => {
        const s = r.status;
        const lastSuccessAgeS = ageS(s.last_success);
        const checkAgeS = ageS(s.check_last);
        const pending = !s.success && !s.error && !s.last_finished && !s.last_success;
        return {
          repo: r.repo,
          engine: s.engine,
          tunnel: s.tunnel === true,
          success: s.success,
          pending,
          error: s.error,
          lastSuccessIso: s.last_success,
          lastFinishedIso: s.last_finished,
          nextRunIso: s.next_run,
          lastSuccessAgeS,
          durationS: s.duration_s ?? null,
          addedBytes: s.added_bytes ?? null,
          totalBytes: s.total_bytes ?? null,
          snapshotCount: s.snapshot_count ?? null,
          checkLastIso: s.check_last,
          checkSuccess: s.check_success,
          checkAgeS,
          backupTone: backupTone(s.success, lastSuccessAgeS, pending),
          checkTone: checkTone(s.check_success, s.check_last, checkAgeS),
          snapshots: s.snapshots ?? []
        };
      })
      .sort((a, b) => a.repo.localeCompare(b.repo))
  );

  const notBackingUp = $derived(!backupsLoading && views.length === 0 && runningRepos.length === 0);
  const hideBackupDetail = $derived(notBackingUp && (isStorageNode || externallyManaged));

  function durationText(secs: number, extraDigits = 0): string {
    if (!isFinite(secs)) return 'n/a';
    const abs = Math.abs(secs);
    if (abs < 60) return `${secs.toFixed(extraDigits)} s`;
    if (abs < 3600) return `${(secs / 60).toFixed(extraDigits)} min`;
    if (abs < 86400) return `${(secs / 3600).toFixed(1 + extraDigits)} h`;
    return `${(secs / 86400).toFixed(1 + extraDigits)} d`;
  }

  function optionalDuration(secs: number | null): string {
    return secs === null ? 'n/a' : durationText(secs);
  }
  function optionalBytes(v: number | null): string {
    return v === null ? 'n/a' : bytes(v);
  }
  function optionalCount(v: number | null): string {
    return v === null ? 'n/a' : Math.round(v).toLocaleString('en-US');
  }
  function absTime(iso?: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '—';
    return d.toLocaleString();
  }
  function runLabel(success: boolean, pending = false): string {
    return pending ? 'scheduled' : success ? 'ok' : 'failed';
  }

  async function loadBackups(manual = false) {
    const gen = ++backupsGen;
    backupsAC?.abort();
    const ac = new AbortController();
    backupsAC = ac;
    if (manual) backupsBusy = true;
    try {
      const resp = await api.backups(hostId, { signal: ac.signal });
      if (gen !== backupsGen) return;
      repos = resp.repos ?? [];
      backupsError = null;
    } catch (e) {
      if (gen !== backupsGen || (e as { name?: string })?.name === 'AbortError') return;
      backupsError = (e as Error).message;
    } finally {
      if (gen === backupsGen) {
        backupsLoading = false;
        backupsBusy = false;
      }
    }
  }

  async function loadCharts() {
    const gen = ++chartsGen;
    chartsAC?.abort();
    const ac = new AbortController();
    chartsAC = ac;
    const zoomed = chartZoom;
    let from: string;
    let to: string | undefined;
    if (zoomed) {
      from = new Date(zoomed.fromMs).toISOString();
      to = new Date(zoomed.toMs).toISOString();
    } else {
      const b = rangeBoundsMs(range);
      from = rangeToFrom(range);
      to = rangeToTo(range);
      fromMs = b.fromMs;
      toMs = b.toMs;
    }
    try {
      const [added, duration] = await Promise.all([
        api.seriesMulti({ host: hostId, metric: 'backup_added_bytes', from, to, splitBy: 'repo', signal: ac.signal }),
        api.seriesMulti({ host: hostId, metric: 'backup_duration_s', from, to, splitBy: 'repo', signal: ac.signal })
      ]);
      if (gen !== chartsGen) return;
      loadedStep = added.step_sec;
      zoomFetched = zoomed !== null;
      addedHistory = added.series;
      durationHistory = duration.series;
      chartsError = null;
      chartsLoading = false;
      masking = false;
    } catch (e) {
      if (gen !== chartsGen || (e as { name?: string })?.name === 'AbortError') return;
      chartsError = (e as Error).message;
      chartsLoading = false;
      masking = false;
    }
  }

  $effect(() => {
    const current = range;
    if (prevRange !== null && !rangeEquals(current, prevRange)) {
      chartZoom = null;
      chartsLoading = true;
      addedHistory = [];
      durationHistory = [];
    }
    prevRange = current;
  });

  $effect(() => {
    void range;
    untrack(() => loadCharts());
  });

  $effect(() => {
    const names = views.map((v) => v.repo);
    untrack(() => {
      if (names.length === 0) {
        selectedRepo = '';
      } else if (!names.includes(selectedRepo)) {
        selectedRepo = names[0];
      }
    });
  });

  onMount(() => {
    loadBackups();
    loadEndpoint();
    loadNodeRole();
    loadServerInfo();
    timer = setInterval(() => {
      if (chartZoom === null) loadCharts();
    }, 10_000);
    liveUnsub = subscribeHost(hostId, ['backup_running', 'backup_run_elapsed_s', 'backup_progress_pct', 'backup_progress_bytes', 'backup_progress_total_bytes'], receiveLive);
    liveTimer = setInterval(() => {
      const now = Date.now();
      let completed = false;
      const next = { ...liveBackups };
      for (const [repo, live] of Object.entries(next)) {
        if (live.running && now - live.lastSeen >= liveFreshMs) {
          next[repo] = { ...live, running: false };
          completed = true;
        }
      }
      liveBackups = next;
      liveNow = now;
      if (completed) refreshAfterRun();
    }, 1000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (liveTimer) clearInterval(liveTimer);
    liveUnsub?.();
    if (copyTimer) clearTimeout(copyTimer);
    chartsAC?.abort();
    backupsAC?.abort();
  });

  const isZoomed = $derived(chartZoom !== null);
  const hasCharts = $derived(addedHistory.length + durationHistory.length > 0);

  function handleZoom(f: number, t: number) {
    chartZoom = { fromMs: f, toMs: t };
    fromMs = f;
    toMs = t;
    if (loadedStep > 0 && chooseStepSec(t - f, sampleIntervalS) < loadedStep) {
      masking = true;
      loadCharts();
    }
  }
  function handleReset() {
    chartZoom = null;
    const b = rangeBoundsMs(range);
    fromMs = b.fromMs;
    toMs = b.toMs;
    if (zoomFetched) {
      masking = true;
      loadCharts();
    }
  }

  const activeRepo = $derived(views.find((v) => v.repo === selectedRepo) ?? null);
  const activeSnapshots = $derived(activeRepo?.snapshots ?? []);
  const sortedSnapshots = $derived(
    snapSort.apply(
      activeSnapshots,
      (s) => (snapSort.key === 'time' ? (s.time ? new Date(s.time).getTime() : 0) : s.id),
      (a, b) => a.id.localeCompare(b.id)
    )
  );
  const selectedSnapshot = $derived(activeSnapshots.find((s) => s.id === selectedSnap) ?? null);

  function selectSnapshot(s: BackupSnapshot) {
    if (selectedSnap === s.id) {
      selectedSnap = null;
      includePaths = [];
      return;
    }
    selectedSnap = s.id;
    includePaths = [];
  }

  function togglePath(p: string) {
    includePaths = includePaths.includes(p) ? includePaths.filter((x) => x !== p) : [...includePaths, p];
  }

  function quotePath(p: string): string {
    return /\s/.test(p) ? `'${p}'` : p;
  }

  const restoreCommand = $derived.by(() => {
    if (!selectedSnapshot) return '';
    const inc = includePaths.map((p) => ` --include ${quotePath(p)}`).join('');
    const bin = isWindows ? `& 'C:\\Program Files\\ServerMonitor\\sm-agent.exe'` : 'sudo /usr/local/bin/sm-agent';
    return `${bin} backup restore --repo ${quotePath(selectedRepo)} --snapshot ${selectedSnapshot.id}${inc}`;
  });

  const stagingTarget = $derived(
    selectedSnapshot
      ? isWindows
        ? `C:\\ProgramData\\ServerMonitor\\restore\\${selectedSnapshot.id}\\`
        : `/var/lib/servermonitor/restore/${selectedSnapshot.id}/`
      : ''
  );

  async function copyCommand() {
    if (copyTimer) clearTimeout(copyTimer);
    try {
      await navigator.clipboard.writeText(restoreCommand);
      copyState = 'copied';
    } catch {
      copyState = 'failed';
    }
    copyTimer = setTimeout(() => (copyState = 'idle'), 1500);
  }
</script>

<div class="space-y-6">
  {#if !hideBackupDetail}
    <p class="text-xs text-zinc-500">
      This host backs up its own files with restic to one or more destinations — this server or an external endpoint.
    </p>
  {/if}

  {#if isStorageNode && nodeRole}
    <div class="rounded-lg border border-sky-900/50 bg-sky-950/20 px-4 py-3">
      <div class="flex items-start gap-2.5">
        <span class="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full {nodeRole.enrolled ? 'bg-emerald-400' : 'bg-amber-400'}"></span>
        <div class="min-w-0">
          <div class="text-sm font-medium text-sky-100">This host is a storage node</div>
          <p class="mt-0.5 text-xs text-zinc-400 leading-relaxed">
            Its agent serves an append-only restic endpoint at
            <span class="font-mono text-zinc-300">{nodeRole.endpoint}:{nodeRole.udp_port}/udp</span>, storing other hosts'
            encrypted backups on its disk — {nodeRole.target_count}
            {nodeRole.target_count === 1 ? 'repository' : 'repositories'}, {bytes(nodeRole.used_bytes)} stored.
            Manage it under <a href="/backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Backups → Storage nodes</a>.
          </p>
        </div>
      </div>
    </div>
  {/if}

  {#if backupStatus?.state === 'stale'}
    <div class="rounded-lg border border-amber-900/50 bg-amber-950/20 px-4 py-3 text-sm text-amber-100/90">
      Backup status is stale{backupStatus.message ? `: ${backupStatus.message}` : ''}.
    </div>
  {:else if backupStatus?.state === 'stale_agent'}
    <div class="rounded-lg border border-amber-900/50 bg-amber-950/20 px-4 py-3 text-sm text-amber-100/90">
      Backup agent binary is outdated{backupStatus.message ? `: ${backupStatus.message}` : ''}. Reconfigure this host to refresh it: <code>{isWindows ? `iex (iwr -useb ${baseUrl}/install.ps1).Content` : `sudo bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`}</code> For offline installs, re-run the install script with --binary (Linux) or -BinaryPath (Windows).
    </div>
  {:else if backupStatus?.state === 'error'}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      Backup status could not be read{backupStatus.message ? `: ${backupStatus.message}` : ''}.
    </div>
  {/if}

  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
    <header class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-3 border-b border-zinc-800">
      <div class="min-w-0">
        <h2 class="text-sm font-medium text-zinc-100">Repositories</h2>
        <p class="mt-0.5 text-[11px] text-zinc-500">Freshness of the last backup and integrity check per repository.</p>
      </div>
      <button
        type="button"
        onclick={() => loadBackups(true)}
        disabled={backupsBusy}
        class="shrink-0 text-xs px-2.5 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
        {backupsBusy ? 'Refreshing…' : 'Refresh'}
      </button>
    </header>

    {#if backupsError}
      <div class="px-4 sm:px-5 py-3 border-b border-rose-900/40 bg-rose-950/30 text-sm text-rose-300">
        Failed to load backup repositories: {backupsError}
      </div>
    {/if}

    {#if backupsLoading}
      <div class="p-4">
        <div class="h-32 rounded-lg shimmer"></div>
      </div>
    {:else if views.length === 0 && !backupsError}
      <div class="p-6 sm:p-8">
        {#if runningRepos.length > 0}
          <h3 class="flex items-center gap-2 text-base font-medium text-zinc-100"><span class="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse"></span>First backup is running <span class="numeric text-sm font-normal text-emerald-300">(started {durationText(displayedElapsed(runningRepos[0]))} ago)</span></h3>
        {:else if isStorageNode}
          <h3 class="text-base font-medium text-zinc-100">This host isn't backing up its own files</h3>
          <p class="mt-1 text-sm text-zinc-500 max-w-xl">
            It's acting as a storage node — a destination that holds other hosts' encrypted backups (see the note above). Storing
            backups for the fleet and backing up its own files are separate roles; this host does only the former.
          </p>
        {:else if externallyManaged}
          <h3 class="text-base font-medium text-zinc-100">This host isn't backing up yet</h3>
          <p class="mt-1 text-sm text-zinc-500 max-w-xl">
            This agent is externally managed (a container image or read-only filesystem). Managed backups <span class="text-zinc-300">are</span>
            supported — a Dockerized agent backs up its host directly. Enable it by setting
            <span class="font-mono text-zinc-300">SM_ENABLE_BACKUP=1</span> plus a repository, then redeploy the container.
          </p>
          <p class="mt-3 text-sm text-zinc-500 max-w-xl">
            Create a repository under <a href="/backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Backups</a>
            to mint the credential, then follow <span class="font-mono text-zinc-600">deploy/AGENT-DOCKER.md → Managed backups</span>.
            A host-installed agent uses <span class="font-mono text-zinc-300">--enable-backup</span> instead.
          </p>
        {:else}
          <h3 class="text-base font-medium text-zinc-100">This host isn't backing up yet</h3>
          <p class="mt-1 text-sm text-zinc-500 max-w-xl">
            {backupStatus?.state === 'not_configured' || !backupStatus
              ? 'Its agent reports no backup job. Enable backups to protect this host and see restore-ready snapshots here.'
              : 'The agent reports backups are enabled but no repository status has arrived yet — the first run may not have finished.'}
          </p>
        {/if}

        {#if !isStorageNode && !externallyManaged}
          {#if endpointConfigured && linkedRepo}
            <div class="mt-4 rounded-lg border border-emerald-900/40 bg-emerald-950/20 px-4 py-3 text-sm text-emerald-100/90">
              A repository <span class="font-mono text-emerald-200">{linkedRepo}</span> for this host already exists on this server.
              Point the agent at it, then it will report here. <a href="/backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Open repositories</a>.
            </div>
          {:else if endpointConfigured}
            <div class="mt-4">
              <a
                href="/backups?new=1&host={hostId}"
                class="inline-flex items-center gap-2 text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 font-medium">
                Back up this host to this server →
              </a>
              <p class="mt-2 text-[11px] text-zinc-600">Creates a repository on this server and shows the exact install command to run on this host.</p>
            </div>
          {/if}

          <div class="mt-5">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">
              {endpointConfigured ? 'Or back up to an external endpoint' : 'Enable backups on this host'}
            </div>
            <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{enableSnippet}</pre>
            <p class="mt-2 text-[11px] text-zinc-500">
              Run this on the host — the installer detects the existing agent and enables backups in place. Full setup — rest-server, S3/B2, TLS, recovery — is in <span class="font-mono text-zinc-600">deploy/BACKUPS.md</span>.
            </p>
          </div>
        {/if}
      </div>
    {:else if views.length > 0}
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-4 sm:px-5 py-2.5">Repository</th>
              <th class="text-left font-medium px-3 py-2.5">Last backup</th>
              <th class="text-left font-medium px-3 py-2.5">Last check</th>
              <th class="text-right font-medium px-3 py-2.5">Size</th>
              <th class="text-right font-medium px-4 sm:px-5 py-2.5">Snapshots</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each views as v (v.repo)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-4 sm:px-5 py-2.5">
                  <div class="flex items-center gap-2 min-w-0">
                    <span class="h-1.5 w-1.5 shrink-0 rounded-full {toneDot[v.backupTone]}"></span>
                    <span class="truncate font-mono text-zinc-100">{v.repo}</span>
                    {#if v.engine}<span class="shrink-0 text-[10px] uppercase tracking-wider text-zinc-500">{v.engine}</span>{/if}
                    {#if v.tunnel}<span class="shrink-0 rounded border border-sky-500/30 bg-sky-500/10 px-1 py-px text-[10px] uppercase tracking-wider text-sky-300" title="Backs up through the WireGuard tunnel to the ServerMonitor server">tunnel</span>{/if}
                  </div>
                </td>
                <td class="px-3 py-2.5">
                  {#if isRunning(v.repo)}
                    <div class="flex items-center gap-2 text-emerald-300"><span class="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse"></span><span>backing up now — started <span class="numeric">{durationText(displayedElapsed(v.repo))}</span> ago</span></div>
                  {:else}
                    <div class="flex items-center gap-2">
                      <span class="numeric {toneText[v.backupTone]}">{v.lastSuccessIso ? timeAgo(v.lastSuccessIso) : v.pending ? 'not yet' : 'never'}</span>
                      {#if v.pending}<span class="text-[10px] uppercase tracking-wider text-zinc-500">scheduled</span>{:else if !v.success}<span class="text-[10px] uppercase tracking-wider text-rose-300">failed</span>{/if}
                    </div>
                  {/if}
                </td>
                <td class="px-3 py-2.5">
                  <div class="flex items-center gap-2">
                    <span class="numeric {toneText[v.checkTone]}">{v.checkLastIso ? timeAgo(v.checkLastIso) : 'never'}</span>
                    {#if v.checkSuccess === false}<span class="text-[10px] uppercase tracking-wider text-rose-300">failed</span>{/if}
                  </div>
                </td>
                <td class="px-3 py-2.5 text-right numeric text-zinc-300">{optionalBytes(v.totalBytes)}</td>
                <td class="px-4 sm:px-5 py-2.5 text-right numeric text-zinc-300">{optionalCount(v.snapshotCount)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4 p-4 border-t border-zinc-800">
        {#each views as v (v.repo)}
          <section class="rounded-xl border border-zinc-800 bg-zinc-950/40 p-4">
            <header class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <h3 class="min-w-0 truncate font-mono text-sm text-zinc-100">{v.repo}</h3>
                {#if v.engine}<div class="mt-0.5 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">{v.engine}{#if v.tunnel}<span class="rounded border border-sky-500/30 bg-sky-500/10 px-1 py-px text-sky-300" title="Backs up through the WireGuard tunnel to the ServerMonitor server">tunnel</span>{/if}</div>{:else if v.tunnel}<div class="mt-0.5 text-[10px] uppercase tracking-wider"><span class="rounded border border-sky-500/30 bg-sky-500/10 px-1 py-px text-sky-300">tunnel</span></div>{/if}
              </div>
              {#if isRunning(v.repo)}
                <span class="shrink-0 rounded-md border border-emerald-900/60 bg-emerald-950/40 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-emerald-300">running</span>
              {:else}
                <span class="shrink-0 rounded-md border px-1.5 py-0.5 text-[10px] uppercase tracking-wider {tonePill[v.backupTone]}">{runLabel(v.success, v.pending)}</span>
              {/if}
            </header>
            {#if isRunning(v.repo)}
              <div class="mt-3">
                <div class="h-1.5 overflow-hidden rounded-full bg-zinc-800"><div class="h-full rounded-full bg-emerald-500" style="width: {liveBackups[v.repo].percent}%"></div></div>
                <div class="mt-1.5 flex items-center justify-between gap-3 text-[11px] text-zinc-400">
                  <span class="text-emerald-300">backing up now — started <span class="numeric">{durationText(displayedElapsed(v.repo))}</span> ago</span>
                  <span class="numeric whitespace-nowrap">{Math.round(liveBackups[v.repo].percent)}% — {liveBackups[v.repo].totalBytes > 0 ? `${bytes(liveBackups[v.repo].bytesDone)} of ${bytes(liveBackups[v.repo].totalBytes)}` : `${bytes(liveBackups[v.repo].bytesDone)} processed`}</span>
                </div>
                {#if liveBackups[v.repo].percent >= 100}<div class="mt-1 text-[10px] text-zinc-500">Data transfer complete; finalizing the backup.</div>{/if}
              </div>
            {/if}
            <div class="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Last success</div>
                <div class="mt-0.5 numeric {toneText[v.backupTone]}">{optionalDuration(v.lastSuccessAgeS)}{v.lastSuccessAgeS !== null ? ' ago' : ''}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Last run</div>
                <div class="mt-0.5 numeric text-zinc-300">{v.lastFinishedIso ? timeAgo(v.lastFinishedIso) : 'n/a'}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Next run</div>
                <div class="mt-0.5 numeric text-zinc-300">{absTime(v.nextRunIso)}{#if v.nextRunIso}<span class="ml-2 text-xs text-zinc-500">{timeUntil(v.nextRunIso)}</span>{/if}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Duration</div>
                <div class="mt-0.5 numeric text-zinc-300">{optionalDuration(v.durationS)}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Added</div>
                <div class="mt-0.5 numeric text-zinc-300">{optionalBytes(v.addedBytes)}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Total</div>
                <div class="mt-0.5 numeric text-zinc-300">{optionalBytes(v.totalBytes)}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Snapshots</div>
                <div class="mt-0.5 numeric text-zinc-300">{optionalCount(v.snapshotCount)}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Check</div>
                <div class="mt-0.5 numeric {toneText[v.checkTone]}">{v.checkLastIso ? (v.checkSuccess === false ? 'failed' : 'passed') : 'n/a'}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Check age</div>
                <div class="mt-0.5 numeric text-zinc-300">{optionalDuration(v.checkAgeS)}</div>
              </div>
            </div>
            {#if !v.success && v.error}
              <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-2.5 py-1.5 text-xs text-rose-300 break-words">{v.error}</div>
            {/if}
          </section>
        {/each}
      </div>
    {/if}
  </section>

  {#if !hideBackupDetail}
  {#if chartsError}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      Failed to load backup history: {chartsError}
    </div>
  {/if}

  {#if chartsLoading}
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      {#each Array(2) as _, i (i)}
        <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
          <header class="px-5 py-3 border-b border-zinc-800"><div class="h-3 w-28 rounded shimmer"></div></header>
          <div class="px-3 py-3"><div class="h-[220px] rounded-md shimmer opacity-60"></div></div>
        </div>
      {/each}
    </div>
  {:else if hasCharts || !chartsError}
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Backup added bytes</div>
          <DownloadCsv host={hostId} metric="backup_added_bytes" splitBy="repo" {range} />
        </header>
        <div class="px-3 py-3">
          <MultiChart series={toSeries(addedHistory)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="B" format={(v, e = 0) => bytes(v, 1 + e)} />
        </div>
      </section>
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Backup duration</div>
          <DownloadCsv host={hostId} metric="backup_duration_s" splitBy="repo" {range} />
        </header>
        <div class="px-3 py-3">
          <MultiChart series={toSeries(durationHistory)} {fromMs} {toMs} zoomed={isZoomed} {masking} onZoom={handleZoom} onResetZoom={handleReset} unit="s" format={durationText} />
        </div>
      </section>
    </div>
  {/if}
  {/if}

  {#if !backupsLoading && !backupsError && views.length > 0}
    <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-3 border-b border-zinc-800">
        <div class="text-xs uppercase tracking-wider text-zinc-500">Snapshots</div>
        {#if views.length > 1}
          <div class="flex flex-wrap items-center gap-1 text-xs">
            {#each views as v (v.repo)}
              <button
                type="button"
                onclick={() => { selectedRepo = v.repo; selectedSnap = null; includePaths = []; }}
                class="px-2 py-1 rounded-md font-mono transition-colors {selectedRepo === v.repo ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
                {v.repo}
              </button>
            {/each}
          </div>
        {/if}
      </header>

      {#if activeSnapshots.length === 0}
        <div class="p-12 text-center">
          <h3 class="text-base font-medium text-zinc-100">No snapshots reported yet</h3>
          <p class="mt-1 text-sm text-zinc-500">Repository <span class="font-mono text-zinc-400">{selectedRepo}</span> has not reported any snapshots.</p>
        </div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
              <tr>
                <th class="text-left font-medium px-4 sm:px-5 py-2.5 w-32" aria-sort={snapSort.ariaSort('id')}>
                  <button type="button" onclick={() => snapSort.toggle('id')} class="uppercase tracking-wider hover:text-zinc-300">Snapshot{snapSort.indicator('id')}</button>
                </th>
                <th class="text-left font-medium px-3 py-2.5" aria-sort={snapSort.ariaSort('time')}>
                  <button type="button" onclick={() => snapSort.toggle('time')} class="uppercase tracking-wider hover:text-zinc-300">Time{snapSort.indicator('time')}</button>
                </th>
                <th class="text-left font-medium px-4 sm:px-5 py-2.5">Paths</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each sortedSnapshots as s (s.id)}
                <tr
                  class="cursor-pointer hover:bg-zinc-900/60 {selectedSnap === s.id ? 'bg-zinc-900/70' : ''}"
                  onclick={() => selectSnapshot(s)}>
                  <td class="px-4 sm:px-5 py-2 font-mono text-zinc-100">{s.id}</td>
                  <td class="px-3 py-2 text-zinc-300">
                    <span class="numeric">{absTime(s.time)}</span>
                    <span class="ml-2 text-xs text-zinc-500 numeric">{s.time ? timeAgo(s.time) : ''}</span>
                  </td>
                  <td class="px-4 sm:px-5 py-2 text-xs text-zinc-400 font-mono truncate max-w-md" title={(s.paths ?? []).join('\n')}>{(s.paths ?? []).join(', ') || '—'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        {#if selectedSnapshot}
          <div class="border-t border-zinc-800 px-4 sm:px-5 py-4 space-y-3">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="text-[11px] uppercase tracking-wider text-zinc-500">Restore command</div>
              <button
                type="button"
                onclick={copyCommand}
                class="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 {copyState === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
                {copyState === 'copied' ? 'copied' : copyState === 'failed' ? 'copy failed — select manually' : 'copy'}
              </button>
            </div>

            {#if (selectedSnapshot.paths ?? []).length > 0}
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1.5">Include paths (optional)</div>
                <div class="flex flex-wrap gap-1.5">
                  {#each selectedSnapshot.paths ?? [] as p (p)}
                    <button
                      type="button"
                      onclick={() => togglePath(p)}
                      aria-pressed={includePaths.includes(p)}
                      class="px-2 py-1 rounded-md border font-mono text-xs transition-colors {includePaths.includes(p) ? 'border-sky-900/60 bg-sky-950/40 text-sky-300' : 'border-zinc-800 bg-zinc-950 text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40'}">
                      {p}
                    </button>
                  {/each}
                </div>
                <p class="mt-1.5 text-[11px] text-zinc-500">Click paths to limit the restore; leave all unselected to restore the full snapshot.</p>
              </div>
            {/if}

            <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre select-text text-zinc-200">{restoreCommand}</pre>

            <p class="text-[11px] text-zinc-500">
              {isWindows ? 'Run in elevated PowerShell.' : 'Run as root on the host.'}
              Restores to <span class="font-mono text-zinc-400">{stagingTarget}</span> by default; no restore is triggered from this UI.
            </p>
          </div>
        {/if}
      {/if}
    </section>
  {/if}
</div>
