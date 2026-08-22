<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import {
    api,
    type CollectorStatus,
    type BackupRepoRow,
    type BackupSnapshot,
    type BackupBrowseResult,
    type BackupTargetsResp,
    type BackupNode
  } from '$lib/api';
  import { type Range } from '$lib/time';
  import { bytes, statusFor, timeAgo, timeUntil } from '$lib/format';
  import { TableSort } from '$lib/sort.svelte';
  import { chartPalette } from '$lib/components/MultiChart.svelte';
  import BackupBarChart, { type BarEvent } from '$lib/components/BackupBarChart.svelte';
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
  const agentPermsCommand = 'sudo chown root:root /usr/local/bin/sm-agent && sudo chmod 0755 /usr/local/bin/sm-agent';
  let agentPermsCopyState = $state<'idle' | 'copied' | 'failed'>('idle');
  let agentPermsCopyTimer: ReturnType<typeof setTimeout> | null = null;

  async function loadServerInfo() {
    try {
      const info = await api.serverInfo();
      if (info?.url) baseUrl = info.url.replace(/\/+$/, '');
    } catch {}
  }

  async function copyAgentPermsCommand() {
    if (agentPermsCopyTimer) clearTimeout(agentPermsCopyTimer);
    try {
      await navigator.clipboard.writeText(agentPermsCommand);
      agentPermsCopyState = 'copied';
    } catch {
      agentPermsCopyState = 'failed';
    }
    agentPermsCopyTimer = setTimeout(() => (agentPermsCopyState = 'idle'), 1500);
  }

  let endpoint = $state<BackupTargetsResp | null>(null);
  const endpointConfigured = $derived(endpoint?.configured ?? false);
  const linkedRepositories = $derived(endpoint?.targets.filter((t) => t.host_id === hostId && !t.revoked_at) ?? []);
  const linkedRepository = $derived(linkedRepositories[0] ?? null);
  const linkedRepo = $derived(linkedRepository?.name ?? null);
  const linkedDirectRepo = $derived(linkedRepositories.length > 0 && linkedRepositories.every((repository) => repository.destination_id != null));
  const hasDirectDestinations = $derived((endpoint?.destinations.length ?? 0) > 0);

  async function loadEndpoint() {
    try {
      endpoint = await api.backupTargets();
    } catch {}
  }

  let nodes = $state<BackupNode[]>([]);
  const nodeRole = $derived(nodes.find((n) => n.host_id === hostId) ?? null);
  const isStorageNode = $derived(nodeRole !== null);
  const canCreateRepo = $derived(endpointConfigured || nodes.length > 0 || hasDirectDestinations);

  async function loadNodeRole() {
    try {
      const resp = await api.backupNodes();
      nodes = resp.nodes;
    } catch {}
  }

  type NodeAvailability = 'removed' | 'archived' | 'offline' | null;

  function nodeAvailability(node: BackupNode | null | undefined): NodeAvailability {
    if (!node) return null;
    if (node.host_missing) return 'removed';
    if (node.archived) return 'archived';
    if (statusFor(node.last_seen, node.sample_interval_s ?? 10) === 'bad') return 'offline';
    return null;
  }

  function storageNodeForRepo(repo: string): BackupNode | null {
    const target = endpoint?.targets.find((candidate) => candidate.host_id === hostId && candidate.name === repo);
    if (target?.node_host_id == null) return null;
    return nodes.find((node) => node.host_id === target.node_host_id) ?? null;
  }

  function registeredTarget(repo: string) {
    return endpoint?.targets.find((candidate) => candidate.host_id === hostId && candidate.name === repo) ?? null;
  }

  function managedDataPath(repo: string): string | null {
    return registeredTarget(repo)?.data_path.label ?? null;
  }

  type TransportBadge = { text: string; title: string; cls: string };

  function transportBadge(repo: string, agentReportedTunnel: boolean): TransportBadge | null {
    const target = registeredTarget(repo);
    switch (target?.data_path.kind) {
      case 'agent_to_s3_direct':
        return {
          text: 's3 direct',
          title: `Agent uploads straight to ${target.storage_backend.location || 'the object store'} over the S3 API. No WireGuard tunnel, and backup data never passes through ServerMonitor.`,
          cls: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
        };
      case 'agent_to_storage_node':
        return {
          text: 'tunnel → node',
          title: `Backs up through the WireGuard tunnel to the storage node ${target.storage_backend.location || ''}`.trim(),
          cls: 'border-sky-500/30 bg-sky-500/10 text-sky-300'
        };
      case 'agent_via_server_to_s3':
      case 'agent_to_server_disk':
      case 'agent_to_server_unavailable':
        return agentReportedTunnel
          ? { text: 'tunnel → server', title: 'Backs up through the WireGuard tunnel to the ServerMonitor server', cls: 'border-sky-500/30 bg-sky-500/10 text-sky-300' }
          : { text: 'server endpoint', title: 'Backs up to the ServerMonitor restic endpoint over HTTPS', cls: 'border-zinc-700 bg-zinc-900 text-zinc-400' };
      default:
        return agentReportedTunnel
          ? { text: 'tunnel', title: 'Backs up through the WireGuard tunnel', cls: 'border-sky-500/30 bg-sky-500/10 text-sky-300' }
          : null;
    }
  }

  function condenseRepoError(error: string): string | null {
    const lower = error.toLowerCase();
    const reasons = ['connection reset by peer', 'connection refused', 'i/o timeout', 'no route to host', 'context deadline exceeded'];
    const reason = reasons.find((candidate) => lower.includes(candidate));
    const retryCount = error.match(/retrying after/gi)?.length ?? 0;
    if (!reason && retryCount === 0) return null;
    if (!reason) return `backup failed after ${retryCount} ${retryCount === 1 ? 'retry' : 'retries'}`;
    const details: string[] = [];
    details.push(reason);
    if (retryCount > 0) details.push(`${retryCount} ${retryCount === 1 ? 'retry' : 'retries'}`);
    return `backup failed: destination unreachable (${details.join('; ')})`;
  }

  const enableSnippet = $derived.by(() => {
    if (linkedDirectRepo) {
      return isWindows
        ? `$env:SM_ENABLE_BACKUP="1"; $env:SM_BACKUP_PRUNE_MODE="external"; iex (iwr -useb ${baseUrl}/install.ps1).Content`
        : `SM_ENABLE_BACKUP='1' SM_BACKUP_PRUNE_MODE='external' \\\n  sudo --preserve-env=SM_ENABLE_BACKUP,SM_BACKUP_PRUNE_MODE bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`;
    }
    return isWindows
      ? `$env:SM_ENABLE_BACKUP="1"; $env:SM_BACKUP_REPOS="<rest/s3 url>"; iex (iwr -useb ${baseUrl}/install.ps1).Content`
      : `SM_ENABLE_BACKUP='1' SM_BACKUP_REPOS='<rest/s3 url>' \\\n  sudo --preserve-env=SM_ENABLE_BACKUP,SM_BACKUP_REPOS bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`;
  });

  type Tone = 'good' | 'warn' | 'bad' | 'none';

  type RepoView = {
    repo: string;
    engine?: string;
    tunnel: boolean;
    success: boolean;
    pending: boolean;
    factsAsOf: string | null;
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
    paths: string[];
    excludes: string[];
    oneFileSystem: boolean | undefined;
    pathStats: { path: string; bytes: number; files: number }[];
    statsSnapshot?: string;
    statsAt?: string;
    snapshots: BackupSnapshot[];
  };

  let repos = $state<BackupRepoRow[]>([]);
  let backupsLoading = $state(true);
  let backupsError = $state<string | null>(null);
  let backupsBusy = $state(false);
  let backupsGen = 0;
  let backupsAC: AbortController | null = null;

  let backupsTimer: ReturnType<typeof setInterval> | null = null;
  let liveTimer: ReturnType<typeof setInterval> | null = null;
  let liveUnsub: (() => void) | null = null;

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
  const snapPageSizes = [8, 16, 50];
  let snapPage = $state(0);
  let snapPageSize = $state(snapPageSizes[0]);
  let copyState = $state<'idle' | 'copied' | 'failed'>('idle');
  let copyTimer: ReturnType<typeof setTimeout> | null = null;
  let browseOpen = $state(false);
  let browsePath = $state('/');
  let browseState = $state<'idle' | 'loading' | 'done' | 'error'>('idle');
  let browseResult = $state<BackupBrowseResult | null>(null);
  let browseCache = $state(new Map<string, BackupBrowseResult>());
  let browseGen = 0;
  let browseAC: AbortController | null = null;
  const backupBrowseClientBudgetMs = 390_000;
  let browseElapsedS = $state(0);
  let browseElapsedTimer: ReturnType<typeof setInterval> | null = null;
  let browseCopyState = $state<'idle' | 'copied' | 'failed'>('idle');
  let browseCopyTimer: ReturnType<typeof setTimeout> | null = null;

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
          factsAsOf: !s.success ? s.last_success ?? null : null,
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
          paths: s.paths ?? [],
          excludes: s.excludes ?? [],
          oneFileSystem: s.one_file_system,
          pathStats: s.path_stats ?? [],
          statsSnapshot: s.stats_snapshot,
          statsAt: s.stats_at,
          snapshots: s.snapshots ?? []
        };
      })
      .sort((a, b) => a.repo.localeCompare(b.repo))
  );

  type ScopeGroup = {
    repos: string[];
    paths: string[];
    excludes: string[];
    oneFileSystem: boolean | undefined;
    stats?: {
      repo: string;
      snapshot: string;
      measuredAt: number | null;
      rows: { path: string; bytes: number; files: number }[];
    };
  };

  const scopeGroups = $derived.by<ScopeGroup[]>(() => {
    const groups = new Map<string, ScopeGroup>();
    for (const view of views) {
      if (view.paths.length === 0 && view.excludes.length === 0 && view.oneFileSystem === undefined) continue;
      const signature = JSON.stringify({ paths: view.paths, excludes: view.excludes, one_file_system: view.oneFileSystem });
      const existing = groups.get(signature);
      const stats = view.pathStats.length > 0 && view.statsSnapshot
        ? {
            repo: view.repo,
            snapshot: view.statsSnapshot,
            measuredAt: view.statsAt ? new Date(view.statsAt).getTime() : null,
            rows: view.pathStats
          }
        : undefined;
      if (existing) {
        existing.repos.push(view.repo);
        if (stats && (!existing.stats || (stats.measuredAt ?? 0) > (existing.stats.measuredAt ?? 0))) existing.stats = stats;
      } else {
        groups.set(signature, { repos: [view.repo], paths: view.paths, excludes: view.excludes, oneFileSystem: view.oneFileSystem, stats });
      }
    }
    return Array.from(groups.values());
  });

  const dockerVolumesWarning = $derived.by<string | null>(() => {
    if (collectorStatus.containers?.state !== 'ok') return null;
    let matchingExclude: string | null = null;
    for (const view of views) {
      if (view.excludes.includes('/var/lib/docker/volumes')) matchingExclude = '/var/lib/docker/volumes';
      else if (!matchingExclude && view.excludes.includes('/var/lib/docker')) matchingExclude = '/var/lib/docker';
    }
    if (!matchingExclude) return null;
    if (views.some((view) => view.paths.some((path) => path.startsWith('/var/lib/docker/volumes')))) return null;
    return matchingExclude;
  });

  let hiddenRepos = $state(new Set<string>());
  const repoNames = $derived(views.map((view) => view.repo));
  const repoColors = $derived.by<Record<string, string>>(() =>
    Object.fromEntries(repoNames.map((repo, index) => [repo, chartPalette[index % chartPalette.length]]))
  );

  function repoColor(repo: string): string {
    return repoColors[repo] ?? chartPalette[0];
  }

  function snapshotEvents(kind: 'added' | 'duration'): BarEvent[] {
    const events: BarEvent[] = [];
    for (const view of views) {
      for (const snapshot of view.snapshots) {
        if (!snapshot.time) continue;
        const timeMs = new Date(snapshot.time).getTime();
        if (!Number.isFinite(timeMs)) continue;
        events.push({
          repo: view.repo,
          timeMs,
          value: kind === 'added' ? snapshot.added_bytes ?? null : snapshot.duration_s ?? null
        });
      }
    }
    return events
      .sort((a, b) => a.timeMs - b.timeMs || a.repo.localeCompare(b.repo))
      .slice(-40);
  }

  const addedEvents = $derived.by(() => snapshotEvents('added'));
  const durationEvents = $derived.by(() => snapshotEvents('duration'));
  const visibleAddedEvents = $derived(addedEvents.filter((event) => !hiddenRepos.has(event.repo)));
  const visibleDurationEvents = $derived(durationEvents.filter((event) => !hiddenRepos.has(event.repo)));

  function toggleRepo(repo: string) {
    const next = new Set(hiddenRepos);
    if (next.has(repo)) next.delete(repo);
    else next.add(repo);
    hiddenRepos = next;
  }

  function csvCell(value: string): string {
    return `"${value.replaceAll('"', '""')}"`;
  }

  function downloadEvents(events: BarEvent[], filename: string) {
    const rows = events.map((event) => [csvCell(event.repo), new Date(event.timeMs).toISOString(), event.value ?? ''].join(','));
    const blob = new Blob([['repo,time,value', ...rows].join('\n') + '\n'], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    anchor.click();
    URL.revokeObjectURL(url);
  }

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
  function shortSnapshotId(id: string): string {
    return id.length > 8 ? id.slice(0, 8) : id;
  }
  function snapshotIdTitle(id: string): string | undefined {
    return id.length > 8 ? id : undefined;
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

  $effect(() => {
    const names = views.map((v) => v.repo);
    untrack(() => {
      if (names.length === 0) {
        if (selectedRepo !== '') resetBrowser();
        selectedRepo = '';
      } else if (!names.includes(selectedRepo)) {
        resetBrowser();
        selectedRepo = names[0];
      }
    });
  });

  onMount(() => {
    loadBackups();
    loadEndpoint();
    loadNodeRole();
    loadServerInfo();
    backupsTimer = setInterval(() => loadBackups(), 60_000);
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
    if (backupsTimer) clearInterval(backupsTimer);
    if (liveTimer) clearInterval(liveTimer);
    liveUnsub?.();
    if (copyTimer) clearTimeout(copyTimer);
    if (agentPermsCopyTimer) clearTimeout(agentPermsCopyTimer);
    if (browseCopyTimer) clearTimeout(browseCopyTimer);
    if (browseElapsedTimer) clearInterval(browseElapsedTimer);
    backupsAC?.abort();
    browseAC?.abort();
  });

  const activeRepo = $derived(views.find((v) => v.repo === selectedRepo) ?? null);
  const activeSnapshots = $derived(activeRepo?.snapshots ?? []);
  const selectedRepoNodeAvailability = $derived(nodeAvailability(storageNodeForRepo(selectedRepo)));
  const sortedSnapshots = $derived(
    snapSort.apply(
      activeSnapshots,
      (s) => (snapSort.key === 'time' ? (s.time ? new Date(s.time).getTime() : 0) : s.id),
      (a, b) => a.id.localeCompare(b.id)
    )
  );
  const selectedSnapshot = $derived(activeSnapshots.find((s) => s.id === selectedSnap) ?? null);

  const snapPageCount = $derived(Math.max(1, Math.ceil(sortedSnapshots.length / snapPageSize)));
  const snapPageIndex = $derived(Math.min(Math.max(snapPage, 0), snapPageCount - 1));
  const snapFrom = $derived(snapPageIndex * snapPageSize);
  const snapTo = $derived(Math.min(snapFrom + snapPageSize, sortedSnapshots.length));
  const pagedSnapshots = $derived(sortedSnapshots.slice(snapFrom, snapTo));

  function clearSnapSelection() {
    selectedSnap = null;
    includePaths = [];
    resetBrowser();
  }

  function gotoSnapPage(page: number) {
    snapPage = Math.min(Math.max(page, 0), snapPageCount - 1);
    clearSnapSelection();
  }

  function setSnapPageSize(size: number) {
    snapPageSize = size;
    snapPage = 0;
    clearSnapSelection();
  }

  function sortSnapshots(key: 'time' | 'id') {
    snapSort.toggle(key);
    snapPage = 0;
    clearSnapSelection();
  }

  function selectSnapshot(s: BackupSnapshot) {
    if (selectedSnap === s.id) {
      selectedSnap = null;
      includePaths = [];
      resetBrowser();
      return;
    }
    selectedSnap = s.id;
    includePaths = [];
    resetBrowser();
  }

  function selectRepo(repo: string) {
    selectedRepo = repo;
    snapPage = 0;
    selectedSnap = null;
    includePaths = [];
    resetBrowser();
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

  function resetBrowser() {
    browseGen++;
    browseAC?.abort();
    browseAC = null;
    if (browseElapsedTimer) clearInterval(browseElapsedTimer);
    browseElapsedTimer = null;
    browseOpen = false;
    browsePath = '/';
    browseState = 'idle';
    browseResult = null;
    browseCache = new Map();
  }

  function browseCacheKey(path: string): string {
    return `${selectedRepo}\0${selectedSnapshot?.id ?? ''}\0${path}`;
  }

  function browseChildPath(name: string): string {
    return browsePath === '/' ? `/${name}` : `${browsePath.replace(/\/$/, '')}/${name}`;
  }

  function browseCrumbs(path: string): { label: string; path: string }[] {
    const crumbs = [{ label: '/', path: '/' }];
    let current = '';
    for (const segment of path.split('/').filter(Boolean)) {
      current += `/${segment}`;
      crumbs.push({ label: segment, path: current });
    }
    return crumbs;
  }

  function browseElapsedText(): string {
    const minutes = Math.floor(browseElapsedS / 60);
    const seconds = browseElapsedS % 60;
    return `${minutes.toString().padStart(2, '0')}:${seconds.toString().padStart(2, '0')}`;
  }

  function browseMaxBytes(scope: ScopeGroup): number {
    return Math.max(0, ...(scope.stats?.rows.map((row) => row.bytes) ?? []));
  }

  function waitForBrowsePoll(signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      const abort = () => {
        clearTimeout(timer);
        reject(new DOMException('Aborted', 'AbortError'));
      };
      const timer = setTimeout(() => {
        signal.removeEventListener('abort', abort);
        resolve();
      }, 1000);
      signal.addEventListener('abort', abort, { once: true });
    });
  }

  async function browseTo(path: string, retry = false) {
    if (!selectedSnapshot) return;
    browseOpen = true;
    browsePath = path;
    const key = browseCacheKey(path);
    if (!retry) {
      const cached = browseCache.get(key);
      if (cached) {
        browseResult = cached;
        browseState = cached.error ? 'error' : 'done';
        return;
      }
    }
    const gen = ++browseGen;
    browseAC?.abort();
    const ac = new AbortController();
    browseAC = ac;
    browseResult = null;
    browseState = 'loading';
    const startedAt = Date.now();
    browseElapsedS = 0;
    if (browseElapsedTimer) clearInterval(browseElapsedTimer);
    const elapsedTimer = setInterval(() => {
      browseElapsedS = Math.floor((Date.now() - startedAt) / 1000);
    }, 1000);
    browseElapsedTimer = elapsedTimer;
    let hardTimedOut = false;
    const hardTimer = setTimeout(() => {
      hardTimedOut = true;
      ac.abort();
    }, backupBrowseClientBudgetMs);
    try {
      const started = await api.backupBrowseStart(hostId, { repo: selectedRepo, snapshot: selectedSnapshot.id, path }, { signal: ac.signal });
      while (Date.now() - startedAt < backupBrowseClientBudgetMs) {
        if (gen !== browseGen) return;
        const job = await api.backupBrowseJob(hostId, started.job_id, { signal: ac.signal });
        if (job.status === 'done' || job.status === 'failed') {
          const result = job.result ?? { error: 'Backup browse job ended without a result.', error_kind: 'failed' };
          if (gen !== browseGen) return;
          browseCache = new Map(browseCache).set(key, result);
          browseResult = result;
          browseState = result.error ? 'error' : 'done';
          return;
        }
        await waitForBrowsePoll(ac.signal);
      }
      if (gen !== browseGen) return;
      const result = { error: 'The backup browse request timed out.', error_kind: 'timed_out' };
      browseResult = result;
      browseState = 'error';
    } catch (e) {
      if (gen !== browseGen) return;
      if (hardTimedOut) {
        browseResult = { error: 'The backup browse request timed out.', error_kind: 'timed_out' };
        browseState = 'error';
        return;
      }
      if ((e as { name?: string })?.name === 'AbortError') return;
      browseResult = { error: (e as Error).message, error_kind: 'failed' };
      browseState = 'error';
    } finally {
      clearTimeout(hardTimer);
      clearInterval(elapsedTimer);
      if (browseElapsedTimer === elapsedTimer) browseElapsedTimer = null;
    }
  }

  function retryBrowse() {
    browseCache.delete(browseCacheKey(browsePath));
    browseCache = new Map(browseCache);
    void browseTo(browsePath, true);
  }

  function browseFallbackCommand(): string {
    const marker = 'fallback command: ';
    const message = browseResult?.error ?? '';
    const index = message.indexOf(marker);
    return index >= 0 ? message.slice(index + marker.length) : message;
  }

  function browseErrorText(): string {
    const message = browseResult?.error ?? 'Backup browse failed.';
    const index = message.indexOf('; fallback command: ');
    return index >= 0 ? message.slice(0, index) : message;
  }

  async function copyBrowseCommand() {
    if (browseCopyTimer) clearTimeout(browseCopyTimer);
    try {
      await navigator.clipboard.writeText(browseFallbackCommand());
      browseCopyState = 'copied';
    } catch {
      browseCopyState = 'failed';
    }
    browseCopyTimer = setTimeout(() => (browseCopyState = 'idle'), 1500);
  }
</script>

<div class="space-y-6">
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
  {:else if backupStatus?.state === 'agent_perms'}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/20 px-4 py-3 text-sm text-rose-100/90">
      <div>{backupStatus.message}</div>
      <div class="mt-2 flex items-center gap-2">
        <code class="min-w-0 flex-1 overflow-x-auto rounded-md bg-zinc-950/70 px-2.5 py-1.5 text-xs text-zinc-200 select-text">{agentPermsCommand}</code>
        <button type="button" onclick={copyAgentPermsCommand} class="shrink-0 text-[11px] px-2 py-1 rounded bg-zinc-800 hover:bg-zinc-700 {agentPermsCopyState === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
          {agentPermsCopyState === 'copied' ? 'copied' : agentPermsCopyState === 'failed' ? 'copy failed' : 'copy'}
        </button>
      </div>
      <div class="mt-2 text-xs text-rose-100/70">Upgrading the agent makes the privileged sync service repair this automatically going forward. Re-running the installer also fixes it.</div>
    </div>
  {:else if backupStatus?.state === 'error'}
    <div class="rounded-lg border border-rose-900/50 bg-rose-950/30 px-4 py-3 text-sm text-rose-300">
      Backup status could not be read{backupStatus.message ? `: ${backupStatus.message}` : ''}.
    </div>
  {/if}

  {#if dockerVolumesWarning}
    <div class="rounded-lg border border-amber-900/50 bg-amber-950/20 px-4 py-3 text-sm text-amber-100/90">
      This host runs Docker containers, but its backups exclude {dockerVolumesWarning} — named volumes are not being backed up. Edit the excludes in /etc/servermonitor-backup/backup.toml (or redeploy a containerized agent to pick up the current default, which keeps /var/lib/docker/volumes).
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
            supported — a Dockerized agent backs up its host directly. Enable the backup runtime with
            <span class="font-mono text-zinc-300">SM_ENABLE_BACKUP=1</span>, choose a centrally managed destination or local repository, then redeploy the container.
          </p>
          <p class="mt-3 text-sm text-zinc-500 max-w-xl">
            Create a repository under <a href="/backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Backups</a>
            to mint the credential, then follow <span class="font-mono text-zinc-600">deploy/AGENT-DOCKER.md → Managed backups</span>.
            A host-installed agent uses <span class="font-mono text-zinc-300">--enable-backup</span> instead.
          </p>
        {:else}
          <h3 class="text-base font-medium text-zinc-100">This host isn't backing up yet</h3>
          <p class="mt-1 text-sm text-zinc-500 max-w-xl">
            {backupStatus?.state === 'scheduled'
              ? 'The agent reports backups are enabled but no repository status has arrived yet — the first run may not have finished.'
              : 'Its agent reports no backup job. Enable backups to protect this host and see restore-ready snapshots here.'}
          </p>
        {/if}

        {#if !isStorageNode && !externallyManaged}
          {#if canCreateRepo && linkedRepo}
            <div class="mt-4 rounded-lg border border-emerald-900/40 bg-emerald-950/20 px-4 py-3 text-sm text-emerald-100/90">
              A repository <span class="font-mono text-emerald-200">{linkedRepo}</span> for this host already exists.
              {linkedDirectRepo
                ? 'Its direct S3 assignment syncs automatically; allow up to one minute, then enable the backup runtime once if this host has never used backups.'
                : 'Point the agent at it, then it will report here.'}
              <a href="/backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Open repositories</a>.
            </div>
          {:else if canCreateRepo}
            <div class="mt-4">
              <a
                href="/backups?new=1&host={hostId}"
                class="inline-flex items-center gap-2 text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 font-medium">
                Choose a backup destination →
              </a>
              <p class="mt-2 text-[11px] text-zinc-600">Creates a repository namespace. Direct S3 assignments sync automatically; gateway destinations show the exact install command.</p>
            </div>
          {/if}

          <div class="mt-5">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">
              {linkedDirectRepo ? 'Enable the managed backup runtime' : canCreateRepo ? 'Or back up to an external endpoint' : 'Enable backups on this host'}
            </div>
            <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{enableSnippet}</pre>
            <p class="mt-2 text-[11px] text-zinc-500">
              Run this on the host — the installer detects the existing agent and enables backups in place. Full setup — rest-server, S3/B2, TLS, recovery — is in <span class="font-mono text-zinc-600">deploy/BACKUPS.md</span>.
            </p>
            {#if linkedDirectRepo}
              <p class="mt-1.5 text-[11px] text-zinc-500">
                Run this after the assignment has had up to one minute to sync. No <span class="font-mono text-zinc-300">SM_BACKUP_REPOS</span> or S3 secret is needed: the endpoint, per-host prefix, and encrypted provider credential are centrally delivered. External pruning avoids giving the host delete authority.
              </p>
            {:else}
              <p class="mt-1.5 text-[11px] text-zinc-500">
                An authenticated <span class="font-mono text-zinc-300">rest:</span> endpoint additionally needs
                <span class="font-mono text-zinc-300">SM_BACKUP_REST_USERNAME</span> and <span class="font-mono text-zinc-300">SM_BACKUP_REST_PASSWORD</span>
                (plus <span class="font-mono text-zinc-300">SM_BACKUP_PRUNE_MODE=external</span> for append-only endpoints) exported the same way and added to <span class="font-mono text-zinc-300">--preserve-env</span>.
              </p>
            {/if}
            {#if !canCreateRepo}
              <p class="mt-1.5 text-[11px] text-zinc-500">
                Add a direct S3/R2 destination, set <span class="font-mono text-zinc-300">BACKUP_DIR</span> or
                <span class="font-mono text-zinc-300">BACKUP_S3_BUCKET</span> for the server gateway, or promote a monitored host into a storage node with
                <span class="font-mono text-zinc-300">BACKUP_WG_PORT</span>; repository namespaces can then be created from
                <a href="/backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Backups</a>.
              </p>
            {/if}
          </div>
        {/if}
      </div>
    {:else if views.length > 0}
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
            <tr>
              <th class="text-left font-medium px-3 sm:px-5 py-2.5">Repository</th>
              <th class="text-left font-medium px-2 sm:px-3 py-2.5">Last backup</th>
              <th class="hidden sm:table-cell text-left font-medium px-2 sm:px-3 py-2.5">Last check</th>
              <th class="hidden sm:table-cell text-right font-medium px-2 sm:px-3 py-2.5">Size</th>
              <th class="text-right font-medium px-3 sm:px-5 py-2.5">Snapshots</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/70">
            {#each views as v (v.repo)}
              {@const badge = transportBadge(v.repo, v.tunnel)}
              <tr class="hover:bg-zinc-900/60">
                <td class="px-3 sm:px-5 py-2.5">
                  <div class="flex items-center gap-2 min-w-0">
                    <span class="h-1.5 w-1.5 shrink-0 rounded-full {toneDot[v.backupTone]}"></span>
                    <span class="truncate font-mono text-zinc-100">{v.repo}</span>
                    {#if v.engine}<span class="hidden sm:inline shrink-0 text-[10px] uppercase tracking-wider text-zinc-500">{v.engine}</span>{/if}
                    {#if badge}<span class="shrink-0 rounded border px-1 py-px text-[10px] uppercase tracking-wider {badge.cls}" title={badge.title}>{badge.text}</span>{/if}
                  </div>
                  {#if managedDataPath(v.repo)}<div class="mt-1 pl-3.5 text-[10px] text-zinc-500">{managedDataPath(v.repo)}</div>{/if}
                </td>
                <td class="px-2 sm:px-3 py-2.5">
                  {#if isRunning(v.repo)}
                    <div class="flex items-center gap-2 text-emerald-300"><span class="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse"></span><span class="whitespace-nowrap">backing up now — started <span class="numeric">{durationText(displayedElapsed(v.repo))}</span> ago</span></div>
                  {:else}
                    <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                      <span class="numeric whitespace-nowrap {toneText[v.backupTone]}">{v.lastSuccessIso ? timeAgo(v.lastSuccessIso) : v.pending ? 'not yet' : 'never'}</span>
                      {#if v.pending}<span class="text-[10px] uppercase tracking-wider text-zinc-500">scheduled</span>{:else if !v.success}<span class="text-[10px] uppercase tracking-wider text-rose-300">failed</span>{/if}
                    </div>
                  {/if}
                </td>
                <td class="hidden sm:table-cell px-2 sm:px-3 py-2.5">
                  <div class="flex items-center gap-2">
                    <span class="numeric {toneText[v.checkTone]}">{v.checkLastIso ? timeAgo(v.checkLastIso) : 'never'}</span>
                    {#if v.checkSuccess === false}<span class="text-[10px] uppercase tracking-wider text-rose-300">failed</span>{/if}
                  </div>
                </td>
                <td class="hidden sm:table-cell px-2 sm:px-3 py-2.5 text-right numeric text-zinc-300" title={v.factsAsOf && v.totalBytes !== null ? `As of the last successful backup (${timeAgo(v.factsAsOf)})` : undefined}>{optionalBytes(v.totalBytes)}</td>
                <td class="px-3 sm:px-5 py-2.5 text-right numeric text-zinc-300" title={v.factsAsOf && v.snapshotCount !== null ? `As of the last successful backup (${timeAgo(v.factsAsOf)})` : undefined}>{optionalCount(v.snapshotCount)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4 p-4 border-t border-zinc-800">
        {#each views as v (v.repo)}
          {@const condensedError = v.error ? condenseRepoError(v.error) : null}
          {@const storageNode = storageNodeForRepo(v.repo)}
          {@const badge = transportBadge(v.repo, v.tunnel)}
          {@const storageNodeAvailability = nodeAvailability(storageNode)}
          <section class="rounded-xl border border-zinc-800 bg-zinc-950/40 p-4">
            <header class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <h3 class="min-w-0 truncate font-mono text-sm text-zinc-100">{v.repo}</h3>
                {#if managedDataPath(v.repo)}<div class="mt-0.5 text-[10px] text-zinc-500">{managedDataPath(v.repo)}</div>{/if}
                {#if v.engine || badge}<div class="mt-0.5 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">{v.engine ?? ''}{#if badge}<span class="rounded border px-1 py-px {badge.cls}" title={badge.title}>{badge.text}</span>{/if}</div>{/if}
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
                <div class="mt-0.5 numeric {toneText[v.backupTone]}">{v.lastSuccessIso ? timeAgo(v.lastSuccessIso) : 'n/a'}</div>
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
                <div class="mt-0.5 numeric text-zinc-300" title={v.factsAsOf && v.totalBytes !== null ? `As of the last successful backup (${timeAgo(v.factsAsOf)})` : undefined}>{optionalBytes(v.totalBytes)}</div>
              </div>
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Snapshots</div>
                <div class="mt-0.5 numeric text-zinc-300" title={v.factsAsOf && v.snapshotCount !== null ? `As of the last successful backup (${timeAgo(v.factsAsOf)})` : undefined}>{optionalCount(v.snapshotCount)}</div>
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
              {#if condensedError}
                <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-2.5 py-1.5 text-xs text-rose-300 break-words">
                  <div>{condensedError}</div>
                  <details class="mt-1.5">
                    <summary class="w-fit cursor-pointer select-none text-[11px] text-zinc-500 hover:text-zinc-300">full output</summary>
                    <pre class="mt-1.5 max-h-40 overflow-y-auto whitespace-pre-wrap break-words border-t border-zinc-800/80 pt-1.5 font-mono text-[11px] text-zinc-400 select-text">{v.error}</pre>
                  </details>
                </div>
              {:else}
                <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-2.5 py-1.5 text-xs text-rose-300 break-words">{v.error}</div>
              {/if}
              {#if storageNode && storageNodeAvailability}
                <p class="mt-2 text-xs text-amber-300">The storage node <span class="font-mono">{storageNode.hostname || `node ${storageNode.host_id}`}</span> hosting this repository appears {storageNodeAvailability}{#if storageNodeAvailability === 'offline' && storageNode.last_seen}{' '}(last seen <span class="numeric">{timeAgo(storageNode.last_seen)}</span>){/if}.{' '}Reconfigure this host to back up to a new repository; if the node's disk is recoverable, its store directory can be copied to a new destination to keep this history.</p>
              {/if}
            {/if}
          </section>
        {/each}
      </div>
      <div class="border-t border-zinc-800 px-4 sm:px-5 py-4">
        {#if scopeGroups.length > 0}
          <div class="text-[10px] uppercase tracking-wider text-zinc-500">Backup scope</div>
          <div class="mt-3 space-y-4">
            {#each scopeGroups as scope (JSON.stringify(scope))}
              <div>
                {#if scopeGroups.length > 1}
                  <div class="mb-2 font-mono text-[11px] text-zinc-400">{scope.repos.join(', ')}</div>
                {/if}
                {#if scope.stats}
                  <div class="space-y-2">
                    {#each scope.stats.rows as stat (stat.path)}
                      <div class="grid grid-cols-[minmax(0,1fr)_minmax(5rem,0.8fr)_auto] items-center gap-3">
                        <span class="truncate font-mono text-xs text-zinc-300" title={stat.path}>{stat.path}</span>
                        <div class="h-1.5 rounded-full bg-zinc-800 overflow-hidden">
                          <div class="h-full rounded-full bg-sky-500/70" style="width: {browseMaxBytes(scope) > 0 ? Math.max(0, Math.min(100, stat.bytes / browseMaxBytes(scope) * 100)) : 0}%"></div>
                        </div>
                        <div class="text-right text-[11px] whitespace-nowrap">
                          <span class="numeric {stat.bytes === 0 ? 'text-amber-300' : 'text-zinc-300'}">{bytes(stat.bytes)}</span>
                          <span class="ml-2 numeric text-zinc-500">{stat.files.toLocaleString('en-US')} files</span>
                        </div>
                      </div>
                    {/each}
                  </div>
                  <div class="mt-2 text-[11px] text-zinc-500 numeric break-words">Measured{scope.stats.measuredAt !== null ? ` ${timeAgo(new Date(scope.stats.measuredAt).toISOString())}` : ''} from snapshot <span class="font-mono text-zinc-400" title={snapshotIdTitle(scope.stats.snapshot)}>{shortSnapshotId(scope.stats.snapshot)}</span> on <span class="font-mono text-zinc-400">{scope.stats.repo}</span>.</div>
                {:else}
                  <div class="flex flex-wrap gap-1.5">
                    {#each scope.paths as path (path)}
                      <span class="px-2 py-1 rounded-md border border-zinc-800 bg-zinc-950 font-mono text-xs text-zinc-300">{path}</span>
                    {/each}
                  </div>
                {/if}
                <div class="mt-3">
                  {#if scope.excludes.length > 0}
                    <details>
                      <summary class="numeric text-xs text-zinc-500 hover:text-zinc-300 cursor-pointer select-none">{scope.excludes.length} exclude {scope.excludes.length === 1 ? 'pattern' : 'patterns'}</summary>
                      <div class="mt-2 flex flex-wrap gap-1.5">
                        {#each scope.excludes as exclude (exclude)}
                          <span class="px-2 py-1 rounded-md border border-zinc-800 bg-zinc-950 font-mono text-xs text-zinc-500">{exclude}</span>
                        {/each}
                      </div>
                    </details>
                  {:else}
                    <div class="text-xs text-zinc-500">No exclude patterns</div>
                  {/if}
                </div>
                {#if scope.oneFileSystem === true}
                  <div class="mt-3 text-[11px] text-zinc-500">Stays on one filesystem — mounts nested under the paths above are not crossed.</div>
                {/if}
              </div>
            {/each}
          </div>
        {:else}
          <div class="text-[11px] text-zinc-600">Scope not reported — agents send backup scope from <span class="numeric">v0.4.0</span> after their next run.</div>
        {/if}
      </div>
    {/if}
  </section>

  {#if !hideBackupDetail && views.length > 0}
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-4 sm:px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Data added per snapshot</div>
          <button
            type="button"
            onclick={() => downloadEvents(visibleAddedEvents, `backup-added-${hostId}.csv`)}
            title="Download snapshot data added as CSV"
            aria-label="Download CSV"
            class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-zinc-500 hover:text-zinc-200 hover:bg-zinc-800/60 transition-colors text-[10px] uppercase tracking-wider">
            <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 3v12" />
              <path d="m7 10 5 5 5-5" />
              <path d="M5 21h14" />
            </svg>
            <span>CSV</span>
          </button>
        </header>
        <div class="px-3 py-3">
          <BackupBarChart events={visibleAddedEvents} format={(v, e = 0) => bytes(v, 1 + e)} {repoColor} tickUnit="bytes" emptyText="No snapshots yet" />
        </div>
      </section>
      <section class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="flex items-center justify-between px-4 sm:px-5 py-3 border-b border-zinc-800">
          <div class="text-xs uppercase tracking-wider text-zinc-500">Duration per snapshot</div>
          <button
            type="button"
            onclick={() => downloadEvents(visibleDurationEvents, `backup-duration-${hostId}.csv`)}
            title="Download snapshot durations as CSV"
            aria-label="Download CSV"
            class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-zinc-500 hover:text-zinc-200 hover:bg-zinc-800/60 transition-colors text-[10px] uppercase tracking-wider">
            <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 3v12" />
              <path d="m7 10 5 5 5-5" />
              <path d="M5 21h14" />
            </svg>
            <span>CSV</span>
          </button>
        </header>
        <div class="px-3 py-3">
          <BackupBarChart events={visibleDurationEvents} format={durationText} {repoColor} tickUnit="seconds" emptyText="No duration reported yet" />
        </div>
      </section>
    </div>
    {#if repoNames.length > 1}
      <div class="flex flex-wrap justify-center gap-x-3 gap-y-1 text-[11px] numeric">
        {#each repoNames as repo (repo)}
          {@const off = hiddenRepos.has(repo)}
          <button
            type="button"
            onclick={() => toggleRepo(repo)}
            aria-pressed={!off}
            title={off ? 'Show repository' : 'Hide repository'}
            class="inline-flex items-center gap-1.5 transition-opacity {off ? 'opacity-40 hover:opacity-70' : 'text-zinc-400 hover:text-zinc-200'}">
            <span class="inline-block h-1.5 w-3 rounded-sm" style="background: {repoColor(repo)}"></span>
            <span class:line-through={off}>{repo}</span>
          </button>
        {/each}
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
                onclick={() => selectRepo(v.repo)}
                class="px-2 py-1 rounded-md font-mono transition-colors {selectedRepo === v.repo ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
                {v.repo}
              </button>
            {/each}
          </div>
        {/if}
      </header>

      {#if selectedRepoNodeAvailability}
        <p class="px-4 pt-3 text-xs text-amber-300 sm:px-5">Snapshot list as of the last successful backup — the destination is unreachable, so restore and browse will fail until the node is back or its store is relocated.</p>
      {/if}

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
                  <button type="button" onclick={() => sortSnapshots('id')} class="uppercase tracking-wider hover:text-zinc-300">Snapshot{snapSort.indicator('id')}</button>
                </th>
                <th class="text-left font-medium px-3 py-2.5" aria-sort={snapSort.ariaSort('time')}>
                  <button type="button" onclick={() => sortSnapshots('time')} class="uppercase tracking-wider hover:text-zinc-300">Time{snapSort.indicator('time')}</button>
                </th>
                <th class="text-right font-medium px-3 py-2.5">Size</th>
                <th class="text-right font-medium px-3 py-2.5">Added</th>
                <th class="hidden sm:table-cell text-right font-medium px-3 py-2.5">Files</th>
                <th class="hidden sm:table-cell text-left font-medium px-4 sm:px-5 py-2.5">Paths</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each pagedSnapshots as s (s.id)}
                <tr
                  class="cursor-pointer transition-colors hover:bg-zinc-900/60 {selectedSnap === s.id ? 'bg-sky-950/20' : ''}"
                  onclick={() => selectSnapshot(s)}>
                  <td class="px-4 sm:px-5 py-2 font-mono text-zinc-100">
                    <button
                      type="button"
                      onclick={(event) => {
                        event.stopPropagation();
                        selectSnapshot(s);
                      }}
                      aria-expanded={selectedSnap === s.id}
                      aria-controls={`snapshot-restore-${s.id}`}
                      class="group inline-flex items-center gap-2 text-left">
                      <svg
                        aria-hidden="true"
                        viewBox="0 0 12 12"
                        fill="none"
                        class="h-3 w-3 shrink-0 transition-transform {selectedSnap === s.id ? 'rotate-90 text-sky-400' : 'text-zinc-600 group-hover:text-zinc-400'}">
                        <path d="M4 2.25 7.75 6 4 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                      </svg>
                      <span>{s.id}</span>
                    </button>
                  </td>
                  <td class="px-3 py-2 text-zinc-300">
                    <span class="numeric">{absTime(s.time)}</span>
                    <span class="ml-2 hidden text-xs text-zinc-500 numeric sm:inline">{s.time ? timeAgo(s.time) : ''}</span>
                  </td>
                  <td class="px-3 py-2 text-right numeric text-zinc-300">{s.size_bytes == null ? '—' : bytes(s.size_bytes)}</td>
                  <td class="px-3 py-2 text-right numeric text-zinc-300">{s.added_bytes == null ? '—' : bytes(s.added_bytes)}</td>
                  <td class="hidden sm:table-cell px-3 py-2 text-right numeric text-zinc-300">{s.file_count == null ? '—' : optionalCount(s.file_count)}</td>
                  <td class="hidden sm:table-cell px-4 sm:px-5 py-2 text-xs text-zinc-400 font-mono truncate max-w-md" title={(s.paths ?? []).join('\n')}>{(s.paths ?? []).join(', ') || '—'}</td>
                </tr>
                {#if selectedSnap === s.id}
                  <tr class="bg-zinc-950/30">
                    <td colspan="6" class="p-0">
                      {@render restorePanel(s)}
                    </td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>

        {#if sortedSnapshots.length > snapPageSizes[0]}
          <div class="flex flex-wrap items-center justify-between gap-2 px-4 sm:px-5 py-2.5 border-t border-zinc-800">
            <div class="flex items-center gap-2">
              <div class="text-[10px] uppercase tracking-wider text-zinc-500">Rows</div>
              <div class="flex items-center gap-0.5">
                {#each snapPageSizes as size (size)}
                  <button
                    type="button"
                    onclick={() => setSnapPageSize(size)}
                    aria-pressed={snapPageSize === size}
                    class="px-2 py-1 rounded-md text-xs font-medium numeric transition-colors {snapPageSize === size ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">{size}</button>
                {/each}
              </div>
            </div>
            <div class="flex items-center gap-2">
              <div class="text-xs text-zinc-500 numeric">{snapFrom + 1}–{snapTo} of {sortedSnapshots.length} · page {snapPageIndex + 1} of {snapPageCount}</div>
              <button
                type="button"
                onclick={() => gotoSnapPage(snapPageIndex - 1)}
                disabled={snapPageIndex === 0}
                aria-label="Newer snapshots"
                title="Newer"
                class="inline-flex items-center rounded-md border border-zinc-800 px-2 py-1.5 text-zinc-400 transition-colors hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-35 disabled:hover:text-zinc-400 disabled:hover:bg-transparent">
                <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
                  <path d="M8 2.25 4.25 6 8 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
              </button>
              <button
                type="button"
                onclick={() => gotoSnapPage(snapPageIndex + 1)}
                disabled={snapPageIndex >= snapPageCount - 1}
                aria-label="Older snapshots"
                title="Older"
                class="inline-flex items-center rounded-md border border-zinc-800 px-2 py-1.5 text-zinc-400 transition-colors hover:text-zinc-100 hover:bg-zinc-800/60 disabled:opacity-35 disabled:hover:text-zinc-400 disabled:hover:bg-transparent">
                <svg aria-hidden="true" viewBox="0 0 12 12" fill="none" class="h-3 w-3">
                  <path d="M4 2.25 7.75 6 4 9.75" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
              </button>
            </div>
          </div>
        {/if}

        {#snippet restorePanel(snapshot: BackupSnapshot)}
          <div
            id={`snapshot-restore-${snapshot.id}`}
            role="region"
            aria-label={`Restore options for snapshot ${snapshot.id}`}
            class="sticky left-0 w-[calc(100vw-3rem)] border-l-2 border-sky-900/70 px-4 py-4 space-y-3 sm:static sm:w-auto sm:px-5">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="flex min-w-0 items-baseline gap-2">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Restore command</div>
                <div class="truncate font-mono text-[11px] text-zinc-400">{snapshot.id}</div>
              </div>
              <button
                type="button"
                onclick={copyCommand}
                class="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 {copyState === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
                {copyState === 'copied' ? 'copied' : copyState === 'failed' ? 'copy failed — select manually' : 'copy'}
              </button>
            </div>

            {#if (snapshot.paths ?? []).length > 0}
              <div>
                <div class="text-[10px] uppercase tracking-wider text-zinc-500 mb-1.5">Include paths (optional)</div>
                <div class="flex flex-wrap gap-1.5">
                  {#each snapshot.paths ?? [] as p (p)}
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

            <div class="pt-1">
              <button
                type="button"
                onclick={() => browseOpen ? resetBrowser() : void browseTo('/')}
                class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                {browseOpen ? 'Close browser' : 'Browse contents'}
              </button>
            </div>

            {#if browseOpen}
              <section class="rounded-lg border border-zinc-800 bg-zinc-950/40 overflow-hidden">
                <div class="flex flex-wrap items-center gap-1 border-b border-zinc-800 px-3 py-2 text-xs font-mono">
                  {#each browseCrumbs(browsePath) as crumb, index (crumb.path)}
                    {#if index > 0}<span class="text-zinc-700">/</span>{/if}
                    <button
                      type="button"
                      onclick={() => void browseTo(crumb.path)}
                      class={crumb.path === browsePath ? 'text-zinc-100' : 'text-zinc-400 hover:text-zinc-200'}>
                      {crumb.label}
                    </button>
                  {/each}
                </div>

                {#if browseState === 'loading'}
                  <div class="px-3 py-3">
                    <div class="h-8 rounded shimmer"></div>
                    <div class="mt-2 flex items-center justify-between gap-3 text-xs text-zinc-500">
                      <span>Asking the agent - it picks up work on its next check-in.</span>
                      <span class="numeric shrink-0">{browseElapsedText()}</span>
                    </div>
                    <div class="mt-1 text-xs text-zinc-500">Large repositories over tunnels can take a few minutes.</div>
                  </div>
                {:else if browseState === 'error'}
                  <div class="px-3 py-3">
                    {#if browseResult?.error_kind === 'busy'}
                      <div class="text-xs text-amber-300">{browseResult?.error ?? 'Backup browse is busy.'}</div>
                      <div class="mt-1 text-xs text-zinc-500">A backup or check is holding the repository lock; try again after it finishes.</div>
                      <button type="button" onclick={retryBrowse} class="mt-2 text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Retry</button>
                    {:else if browseResult?.error_kind === 'insufficient_privilege'}
                      <div class="text-xs text-amber-300">{browseErrorText()}</div>
                      <div class="mt-2 flex items-center justify-between gap-2">
                        <div class="text-[10px] uppercase tracking-wider text-zinc-500">Run on the host</div>
                        <button type="button" onclick={copyBrowseCommand} class="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 {browseCopyState === 'failed' ? 'text-rose-300' : 'text-zinc-200'}">
                          {browseCopyState === 'copied' ? 'copied' : browseCopyState === 'failed' ? 'copy failed — select manually' : 'copy'}
                        </button>
                      </div>
                      <pre class="mt-1 text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre select-text text-zinc-200">{browseFallbackCommand()}</pre>
                    {:else if browseResult?.error_kind === 'timed_out'}
                      <div class="text-xs text-amber-300">
                        <div>{browseResult?.error}</div>
                        <div class="mt-1">Retry is usually faster: the agent keeps a warm cache, and a finished result may already be waiting.</div>
                      </div>
                      <button type="button" onclick={retryBrowse} class="mt-2 text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Retry</button>
                    {:else if browseResult?.error_kind === 'agent_unreachable'}
                      <div class="text-xs text-rose-300">{browseResult?.error}</div>
                      <button type="button" onclick={retryBrowse} class="mt-2 text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Retry</button>
                    {:else}
                      <div class="text-xs text-rose-300">{browseResult?.error ?? 'Backup browse failed.'}</div>
                      <button type="button" onclick={retryBrowse} class="mt-2 text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Retry</button>
                    {/if}
                  </div>
                {:else if browseState === 'done'}
                  {#if (browseResult?.entries ?? []).length === 0}
                    <div class="px-3 py-4 text-xs text-zinc-500">Empty directory.</div>
                  {:else}
                    <div class="overflow-x-auto">
                      <table class="w-full text-sm">
                        <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
                          <tr>
                            <th class="text-left font-medium px-3 py-2">Name</th>
                            <th class="text-right font-medium px-3 py-2">Size</th>
                            <th class="text-left font-medium px-3 py-2">Modified</th>
                          </tr>
                        </thead>
                        <tbody class="divide-y divide-zinc-800/70">
                          {#each browseResult?.entries ?? [] as entry (`${entry.type}:${entry.name}`)}
                            <tr
                              onclick={() => entry.type === 'dir' && void browseTo(browseChildPath(entry.name))}
                              class={entry.type === 'dir' ? 'cursor-pointer hover:bg-zinc-900/60' : ''}>
                              <td class="px-3 py-2 font-mono {entry.type === 'dir' ? 'text-sky-300' : 'text-zinc-300'}">{entry.name}{entry.type === 'dir' ? '/' : ''}</td>
                              <td class="px-3 py-2 text-right numeric text-zinc-300">{entry.type === 'dir' ? '—' : bytes(entry.size ?? 0)}</td>
                              <td class="px-3 py-2 numeric text-zinc-400">{entry.mtime ? new Date(entry.mtime).toLocaleString() : '—'}</td>
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                  {/if}
                  {#if browseResult?.truncated}
                    <div class="border-t border-zinc-800 px-3 py-2 text-[11px] text-amber-300/80">Showing the first 2,000 entries.</div>
                  {/if}
                {/if}
              </section>
            {/if}
          </div>
        {/snippet}
      {/if}
    </section>
  {/if}
</div>
