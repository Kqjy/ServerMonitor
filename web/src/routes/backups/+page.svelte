<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import {
    api,
    type BackupTargetsResp,
    type BackupTarget,
    type BackupCredential,
    type BackupTunnelResp,
    type BackupTunnelPeer,
    type BackupNode,
    type Host
  } from '$lib/api';
  import { bytes, timeAgo } from '$lib/format';
  import { subscribeHosts, type LivePoint } from '$lib/sse';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  type View = 'list' | 'new';
  type SnippetTab = 'linux' | 'windows' | 'docker' | 'restic' | 'existing';

  let view = $state<View>('list');
  let data = $state<BackupTargetsResp | null>(null);
  let tunnel = $state<BackupTunnelResp | null>(null);
  let nodes = $state<BackupNode[]>([]);
  let hosts = $state<Host[]>([]);
  let peerToRevoke = $state<BackupTunnelPeer | null>(null);
  let nodeToDemote = $state<BackupNode | null>(null);
  let demoteError = $state<string | null>(null);

  let promoting = $state(false);
  let promoteHostId = $state<number | null>(null);
  let promoteEndpoint = $state('');
  let promotePort = $state<number>(51821);
  let promoteStoreDir = $state('');
  let promoteError = $state<string | null>(null);
  let promoteBusy = $state(false);

  let editingNode = $state<BackupNode | null>(null);
  let editEndpoint = $state('');
  let editPort = $state<number>(51821);
  let editError = $state<string | null>(null);
  let editBusy = $state(false);

  let newDestination = $state<'server' | number>('server');
  let baseUrl = $state('');
  let loading = $state(true);
  let error = $state<string | null>(null);
  let measuring = $state<number | null>(null);

  let newName = $state('');
  let newHostId = $state<number | null>(null);
  let newQuotaGiB = $state<number | null>(null);
  let creating = $state(false);
  let createError = $state<string | null>(null);
  let credential = $state<BackupCredential | null>(null);
  let snippetTab = $state<SnippetTab>('linux');
  let copied = $state<string | null>(null);

  let rotated = $state<BackupCredential | null>(null);
  let toRevoke = $state<BackupTarget | null>(null);
  let toDelete = $state<BackupTarget | null>(null);

  let firstBlobSeen = $state(false);
  let poll: ReturnType<typeof setInterval> | null = null;
  let hostsTimer: ReturnType<typeof setInterval> | null = null;
  let liveTimer: ReturnType<typeof setInterval> | null = null;
  let liveUnsub: (() => void) | null = null;
  let live = $state<Record<number, Record<string, { running: boolean; lastSeen: number }>>>({});
  let liveNow = $state(Date.now());

  async function load() {
    loading = true;
    error = null;
    try {
      const [resp, info, tun, nodeResp] = await Promise.all([
        api.backupTargets(),
        api.serverInfo(),
        api.backupTunnel().catch(() => null),
        api.backupNodes().catch(() => null)
      ]);
      data = resp;
      tunnel = tun;
      nodes = nodeResp?.nodes ?? [];
      baseUrl = (info.url || (typeof window !== 'undefined' ? window.location.origin : '')).replace(/\/+$/, '');
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
    void loadHosts();
  }

  async function revokePeer() {
    if (!peerToRevoke) return;
    await api.backupTunnelPeerRevoke(peerToRevoke.host_id);
    peerToRevoke = null;
    try {
      tunnel = await api.backupTunnel();
    } catch {}
  }

  function openPromote() {
    editingNode = null;
    promoteHostId = null;
    promoteEndpoint = '';
    promotePort = 51821;
    promoteStoreDir = '';
    promoteError = null;
    promoting = true;
  }

  function openEdit(node: BackupNode) {
    promoting = false;
    editingNode = node;
    editEndpoint = node.endpoint;
    editPort = node.udp_port || 51821;
    editError = null;
  }

  async function saveEdit() {
    if (!editingNode) return;
    if (!editEndpoint.trim()) {
      editError = 'Give the endpoint agents reach this node at.';
      return;
    }
    editBusy = true;
    editError = null;
    try {
      await api.backupNodeUpdate(editingNode.host_id, {
        endpoint: editEndpoint.trim(),
        udp_port: editPort || 51821
      });
      editingNode = null;
      const nodeResp = await api.backupNodes().catch(() => null);
      nodes = nodeResp?.nodes ?? nodes;
    } catch (e) {
      editError = (e as Error).message;
    } finally {
      editBusy = false;
    }
  }

  function promoteHostChanged() {
    if (promoteHostId != null && !promoteEndpoint) {
      const h = hosts.find((x) => x.id === promoteHostId);
      if (h) promoteEndpoint = h.hostname;
    }
  }

  async function promote() {
    if (promoteHostId == null || !promoteEndpoint.trim()) {
      promoteError = 'Pick a host and give the endpoint agents reach it at.';
      return;
    }
    const storeDir = promoteStoreDir.trim();
    if (storeDir && !storeDir.startsWith('/')) {
      promoteError = 'Store directory must be an absolute path starting with /.';
      return;
    }
    promoteBusy = true;
    promoteError = null;
    try {
      await api.backupNodePromote({
        host_id: promoteHostId,
        endpoint: promoteEndpoint.trim(),
        udp_port: promotePort || 51821,
        ...(storeDir ? { store_dir: storeDir } : {})
      });
      promoting = false;
      const nodeResp = await api.backupNodes().catch(() => null);
      nodes = nodeResp?.nodes ?? nodes;
    } catch (e) {
      promoteError = (e as Error).message;
    } finally {
      promoteBusy = false;
    }
  }

  async function demoteNode() {
    if (!nodeToDemote) return;
    demoteError = null;
    try {
      await api.backupNodeDemote(nodeToDemote.host_id);
      nodeToDemote = null;
      const nodeResp = await api.backupNodes().catch(() => null);
      nodes = nodeResp?.nodes ?? nodes;
    } catch (e) {
      demoteError = (e as Error).message;
      nodeToDemote = null;
    }
  }

  const promotableHosts = $derived(hosts.filter((h) => !nodes.some((n) => n.host_id === h.id)));
  const selectedNode = $derived(newDestination === 'server' ? null : (nodes.find((n) => n.host_id === newDestination) ?? null));

  async function loadHosts() {
    try {
      hosts = await api.hosts();
    } catch {}
  }

  function sanitizeName(s: string): string {
    return s.trim().replace(/[^a-zA-Z0-9._-]+/g, '-').replace(/^[-.]+|[-.]+$/g, '');
  }

  function openNew(hostId?: number) {
    newName = '';
    newHostId = hostId ?? null;
    newQuotaGiB = null;
    newDestination = 'server';
    credential = null;
    createError = null;
    firstBlobSeen = false;
    snippetTab = 'linux';
    view = 'new';
    void loadHosts().then(() => {
      if (hostId != null) {
        const h = hosts.find((x) => x.id === hostId);
        if (h && !newName) newName = sanitizeName(h.hostname);
      }
    });
  }

  function backToList() {
    stopPoll();
    view = 'list';
    void load();
  }

  const nameValid = $derived(/^[a-zA-Z0-9._-]+$/.test(newName.trim()) && newName.trim() !== '.' && newName.trim() !== '..');

  async function create() {
    if (!nameValid) {
      createError = 'Name must use only letters, digits, dot, dash or underscore.';
      return;
    }
    if (newDestination !== 'server' && newHostId == null) {
      createError = 'Node-hosted repositories need a linked host — the node only admits tunnel peers that own a repository on it.';
      return;
    }
    creating = true;
    createError = null;
    try {
      const body: { name: string; host_id?: number; quota_bytes?: number; node_host_id?: number } = { name: newName.trim() };
      if (newHostId != null) body.host_id = newHostId;
      if (newQuotaGiB != null && newQuotaGiB > 0) body.quota_bytes = Math.round(newQuotaGiB * 1024 ** 3);
      if (newDestination !== 'server') body.node_host_id = newDestination;
      credential = await api.backupTargetCreate(body);
      startPoll();
    } catch (e) {
      createError = (e as Error).message;
    } finally {
      creating = false;
    }
  }

  function startPoll() {
    stopPoll();
    poll = setInterval(async () => {
      if (!credential) return;
      try {
        const resp = await api.backupTargets();
        data = resp;
        const t = resp.targets.find((x) => x.id === credential!.id);
        if (t && t.used_bytes > 0) firstBlobSeen = true;
      } catch {}
    }, 2500);
  }

  function stopPoll() {
    if (poll) clearInterval(poll);
    poll = null;
  }

  async function measure(id: number) {
    measuring = id;
    try {
      const updated = await api.backupTargetMeasure(id);
      if (data) data.targets = data.targets.map((t) => (t.id === id ? updated : t));
    } catch (e) {
      error = (e as Error).message;
    } finally {
      measuring = null;
    }
  }

  async function rotate(id: number) {
    try {
      rotated = await api.backupTargetRotate(id);
      await load();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function doRevoke() {
    if (!toRevoke) return;
    await api.backupTargetRevoke(toRevoke.id);
    toRevoke = null;
    await load();
  }

  async function doDelete() {
    if (!toDelete) return;
    await api.backupTargetDelete(toDelete.id);
    toDelete = null;
    await load();
  }

  function copy(text: string, key: string) {
    void navigator.clipboard?.writeText(text).catch(() => {});
    copied = key;
    setTimeout(() => (copied = null), 1500);
  }

  function sharePct(used: number, quota?: number): number {
    if (!quota || quota <= 0) return 0;
    return Math.min(100, Math.round((used / quota) * 100));
  }

  function barTone(used: number, quota?: number): string {
    const pct = sharePct(used, quota);
    if (pct >= 95) return 'bg-rose-500/70';
    if (pct >= 80) return 'bg-amber-500/70';
    return 'bg-sky-500/70';
  }

  type HostBackup = {
    id: number;
    hostname: string;
    state: string;
    message?: string;
    repos: string[];
  };

  const reposByHost = $derived.by(() => {
    const m = new Map<number, string[]>();
    for (const t of data?.targets ?? []) {
      if (t.host_id == null || t.revoked_at) continue;
      const arr = m.get(t.host_id) ?? [];
      arr.push(t.name);
      m.set(t.host_id, arr);
    }
    return m;
  });

  const activeBackupStates = new Set(['ok', 'stale', 'error', 'scheduled', 'stale_agent']);
  const hostBackups = $derived.by<HostBackup[]>(() =>
    hosts
      .filter((h) => {
        const state = h.collector_status?.backup?.state ?? '';
        return activeBackupStates.has(state) && !(state === 'stale_agent' && nodes.some((n) => n.host_id === h.id));
      })
      .map((h) => ({
        id: h.id,
        hostname: h.hostname,
        state: h.collector_status!.backup.state,
        message: h.collector_status!.backup.message,
        repos: reposByHost.get(h.id) ?? []
      }))
      .sort((a, b) => a.hostname.localeCompare(b.hostname))
  );

  function stateDot(state: string): string {
    switch (state) {
      case 'ok':
        return 'bg-emerald-400';
      case 'stale':
      case 'stale_agent':
        return 'bg-amber-400';
      case 'error':
        return 'bg-rose-400';
      case 'scheduled':
        return 'bg-sky-400';
      default:
        return 'bg-zinc-600';
    }
  }
  function stateText(state: string): string {
    switch (state) {
      case 'ok':
        return 'text-emerald-300';
      case 'stale':
      case 'stale_agent':
        return 'text-amber-300';
      case 'error':
        return 'text-rose-300';
      case 'scheduled':
        return 'text-sky-300';
      default:
        return 'text-zinc-500';
    }
  }
  function stateLabel(state: string): string {
    switch (state) {
      case 'ok':
        return 'idle';
      case 'stale':
        return 'stale';
      case 'stale_agent':
        return 'stale agent';
      case 'error':
        return 'error';
      case 'scheduled':
        return 'scheduled';
      case 'not_configured':
        return 'not configured';
      default:
        return state || 'unknown';
    }
  }

  function receiveLive(hostId: number, points: LivePoint[]) {
    const hostLive = { ...(live[hostId] ?? {}) };
    const now = Date.now();
    for (const point of points) {
      const repo = point.labels?.repo ?? '';
      hostLive[repo] = { running: point.v === 1, lastSeen: now };
    }
    live = { ...live, [hostId]: hostLive };
    liveNow = now;
  }

  function hostIsRunning(hostId: number): boolean {
    return Object.values(live[hostId] ?? {}).some((entry) => entry.running && liveNow - entry.lastSeen < 30_000);
  }

  const tunnelActive = $derived(tunnel?.enabled === true);
  const publicRepoUrl = $derived(credential ? `rest:${baseUrl}/backup/${credential.name}` : '');
  const repoSpec = $derived.by(() => {
    if (!credential) return '';
    if (selectedNode) return `tunnel:${selectedNode.hostname}/${credential.name}`;
    return tunnelActive ? `tunnel:${credential.name}` : publicRepoUrl;
  });
  const repoUrl = $derived(selectedNode || (tunnelActive && !tunnel?.public_http) ? repoSpec : publicRepoUrl);
  const linkedHostname = $derived(newHostId != null ? (hosts.find((h) => h.id === newHostId)?.hostname ?? '') : '');

  const linuxSnippet = $derived.by(() => {
    if (!credential) return '';
    const vars = [
      'SM_ADMIN_TOKEN=<ADMIN_TOKEN>',
      'SM_ENABLE_BACKUP=1',
      `SM_BACKUP_REPOS="${repoSpec}"`,
      `SM_BACKUP_REPO_NAMES="${credential.name}"`,
      `SM_BACKUP_REST_USERNAME="${credential.name}"`,
      `SM_BACKUP_REST_PASSWORD="${credential.password}"`,
      'SM_BACKUP_PRUNE_MODE=external'
    ];
    const preserve = vars.map((v) => v.split('=')[0]).join(',');
    return `${vars.join(' \\\n  ')} \\\n  sudo --preserve-env=${preserve} bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`;
  });

  const windowsSnippet = $derived.by(() => {
    if (!credential) return '';
    return [
      '$env:SM_ADMIN_TOKEN="<ADMIN_TOKEN>"',
      '$env:SM_ENABLE_BACKUP="1"',
      `$env:SM_BACKUP_REPOS="${repoSpec}"`,
      `$env:SM_BACKUP_REPO_NAMES="${credential.name}"`,
      `$env:SM_BACKUP_REST_USERNAME="${credential.name}"`,
      `$env:SM_BACKUP_REST_PASSWORD="${credential.password}"`,
      '$env:SM_BACKUP_PRUNE_MODE="external"',
      `iex (iwr -useb ${baseUrl}/install.ps1).Content`
    ].join('\n');
  });

  const dockerSnippet = $derived.by(() => {
    if (!credential) return '';
    const lines = [
      'SM_ENABLE_BACKUP=1',
      `SM_BACKUP_REPOS="${repoSpec}"`
    ];
    if (repoSpec.startsWith('rest:')) {
      lines.push(`SM_BACKUP_REST_USERNAME="${credential.name}"`);
      lines.push(`SM_BACKUP_REST_PASSWORD="${credential.password}"`);
    }
    lines.push(
      'SM_BACKUP_TIME=02:30',
      'TZ=UTC'
    );
    return lines.join('\n');
  });

  const dockerRedeployCommand = 'docker compose -f deploy/docker-compose.agent.yml --env-file .env.agent up -d';

  const existingToml = $derived.by(() => {
    if (!credential) return '';
    let source = `url = "${publicRepoUrl}"`;
    if (selectedNode) {
      source = `tunnel_name = "${credential.name}"
tunnel_node = "${selectedNode.hostname}"`;
    } else if (tunnelActive) {
      source = `tunnel_name = "${credential.name}"`;
    }
    return `[[repo]]
name = "${credential.name}"
${source}
password_file = "/etc/servermonitor-backup/backup.key"
env_file = "/etc/servermonitor-backup/repo-credentials.env"`;
  });

  const existingEnv = $derived.by(() => {
    if (!credential) return '';
    return `RESTIC_REST_USERNAME=${credential.name}
RESTIC_REST_PASSWORD=${credential.password}`;
  });

  const resticSnippet = $derived.by(() => {
    if (!credential) return '';
    if (selectedNode || (tunnelActive && !tunnel?.public_http)) {
      return `export RESTIC_REST_USERNAME=${credential.name}
export RESTIC_REST_PASSWORD=${credential.password}
sm-agent backup proxy --config /etc/servermonitor-backup/backup.toml
restic -r <printed RESTIC_REPOSITORY> snapshots`;
    }
    return `export RESTIC_REST_USERNAME=${credential.name}
export RESTIC_REST_PASSWORD=${credential.password}
restic -r ${publicRepoUrl} init
restic -r ${publicRepoUrl} backup /etc`;
  });

  const activeSnippet = $derived(
    snippetTab === 'linux'
      ? linuxSnippet
      : snippetTab === 'windows'
        ? windowsSnippet
        : snippetTab === 'docker'
          ? dockerSnippet
          : resticSnippet
  );

  onMount(async () => {
    await load();
    hostsTimer = setInterval(() => void loadHosts(), 30_000);
    liveUnsub = subscribeHosts(['backup_running'], receiveLive);
    liveTimer = setInterval(() => (liveNow = Date.now()), 5_000);
    const sp = $page.url.searchParams;
    if (sp.get('new') === '1' && data?.configured) {
      const h = sp.get('host');
      const hid = h != null ? Number(h) : NaN;
      openNew(Number.isInteger(hid) && hid > 0 ? hid : undefined);
    }
  });
  onDestroy(() => {
    stopPoll();
    if (hostsTimer) clearInterval(hostsTimer);
    if (liveTimer) clearInterval(liveTimer);
    liveUnsub?.();
  });
</script>

{#snippet howBackupsContent()}
  <p class="mt-1.5 text-xs text-zinc-400 leading-relaxed max-w-3xl">
    Monitored hosts back up their own files with restic — encrypted on the host, so the destination only ever sees
    ciphertext. A destination can be <span class="text-zinc-200">this server</span> or an
    <span class="text-zinc-200">external</span> rest-server / S3 endpoint. Enable a host's backups at install with
    <code class="font-mono text-zinc-300">--enable-backup</code>, then watch each host under its
    <span class="text-zinc-200">Backups</span> tab.
  </p>
  <div class="mt-4 flex flex-wrap items-stretch gap-2 text-xs">
    <div class="rounded-lg border border-zinc-800 bg-zinc-950/60 px-3 py-2 flex items-center">
      <div>
        <div class="text-zinc-200 font-medium">Monitored hosts</div>
        <div class="text-[11px] text-zinc-500 mt-0.5">run <code class="font-mono">sm-agent backup</code></div>
      </div>
    </div>
    <div class="flex items-center px-1 text-zinc-600">
      <div class="text-center">
        <div class="text-[10px] uppercase tracking-wider text-zinc-500">encrypted restic</div>
        <div class="text-sky-400/70 text-base leading-none">&rarr;</div>
      </div>
    </div>
    <div class="rounded-lg border border-zinc-800 bg-zinc-950/60 px-3 py-2 flex-1 min-w-[220px]">
      <div class="text-zinc-200 font-medium">Destination</div>
      <div class="mt-1 space-y-0.5 text-[11px] text-zinc-400">
        <div class="flex items-center gap-1.5"><span class="h-1 w-1 rounded-full bg-emerald-400"></span> This server (append-only endpoint{#if tunnelActive}, over WireGuard{/if})</div>
        <div class="flex items-center gap-1.5"><span class="h-1 w-1 rounded-full bg-zinc-500"></span> External rest-server VPS or S3 / B2</div>
      </div>
    </div>
  </div>
{/snippet}

{#snippet howBackupsWork()}
  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
    <h2 class="text-sm font-medium text-zinc-100">How backups work</h2>
    {@render howBackupsContent()}
  </section>
{/snippet}

{#snippet howBackupsDetails()}
  <details class="group rounded-xl border border-zinc-800 bg-zinc-900/40">
    <summary class="flex cursor-pointer select-none list-none items-center justify-between gap-3 px-4 sm:px-5 py-3 text-sm font-medium text-zinc-100 [&::-webkit-details-marker]:hidden">
      <span>How backups work</span>
      <svg viewBox="0 0 20 20" class="h-4 w-4 text-zinc-500 transition-transform group-open:rotate-180" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="m6 8 4 4 4-4" />
      </svg>
    </summary>
    <div class="p-4 sm:p-5 pt-0">
      {@render howBackupsContent()}
    </div>
  </details>
{/snippet}

{#snippet fleetSection()}
  <section>
    <h2 class="text-sm font-medium text-zinc-100">Hosts backing themselves up</h2>
    <p class="mt-0.5 text-xs text-zinc-500">Monitored hosts whose agents report a backup job — wherever the destination is.</p>

    <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      {#if hostBackups.length === 0}
        <div class="px-4 py-10 text-center text-sm text-zinc-500">
          No monitored hosts report backup jobs yet. Enable a host with <code class="font-mono text-zinc-400">--enable-backup</code>{#if data?.configured}, or create a repository above and point a host at it{/if}.
          <div class="mt-3"><a href="/" class="text-xs text-sky-300 hover:text-sky-200">View hosts →</a></div>
        </div>
      {:else}
        <div class="hidden md:block overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                <th class="px-4 py-2.5 font-medium">Host</th>
                <th class="px-4 py-2.5 font-medium">Backup status</th>
                <th class="px-4 py-2.5 font-medium">Repository</th>
                <th class="px-4 py-2.5 font-medium text-right"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each hostBackups as h (h.id)}
                {@const running = hostIsRunning(h.id)}
                <tr class="hover:bg-zinc-900/60">
                  <td class="px-4 py-3">
                    <a href="/hosts/{h.id}?tab=backups" class="font-mono text-zinc-200 hover:text-zinc-100">{h.hostname}</a>
                  </td>
                  <td class="px-4 py-3">
                    <div class="flex items-center gap-2">
                      <span class="h-1.5 w-1.5 rounded-full {running ? 'bg-emerald-400 animate-pulse' : stateDot(h.state)}"></span>
                      <span class="text-xs {running ? 'text-emerald-300' : stateText(h.state)}">{running ? 'backing up now' : stateLabel(h.state)}</span>
                    </div>
                    {#if h.message}<div class="mt-0.5 text-[11px] text-zinc-600 truncate max-w-xs" title={h.message}>{h.message}</div>{/if}
                  </td>
                  <td class="px-4 py-3 text-xs">
                    {#if h.repos.length > 0}
                      <span class="text-emerald-300/90 font-mono">{h.repos.join(', ')}</span>
                    {:else}
                      <span class="text-zinc-600">external / not linked</span>
                    {/if}
                  </td>
                  <td class="px-4 py-3 text-right">
                    <a href="/hosts/{h.id}?tab=backups" class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Backups tab</a>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <div class="md:hidden divide-y divide-zinc-800/70">
          {#each hostBackups as h (h.id)}
            {@const running = hostIsRunning(h.id)}
            <div class="px-4 py-3 space-y-2">
              <div class="flex items-center justify-between gap-3">
                <a href="/hosts/{h.id}?tab=backups" class="min-w-0 truncate font-mono text-sm text-zinc-200 hover:text-zinc-100">{h.hostname}</a>
                <a href="/hosts/{h.id}?tab=backups" class="shrink-0 text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Backups tab</a>
              </div>
              <div>
                <div class="flex items-center gap-2">
                  <span class="h-1.5 w-1.5 rounded-full {running ? 'bg-emerald-400 animate-pulse' : stateDot(h.state)}"></span>
                  <span class="text-xs {running ? 'text-emerald-300' : stateText(h.state)}">{running ? 'backing up now' : stateLabel(h.state)}</span>
                </div>
                {#if h.message}<div class="mt-0.5 text-[11px] text-zinc-600 truncate" title={h.message}>{h.message}</div>{/if}
              </div>
              <div class="text-xs">
                {#if h.repos.length > 0}
                  <span class="text-emerald-300/90 font-mono">{h.repos.join(', ')}</span>
                {:else}
                  <span class="text-zinc-600">external / not linked</span>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </section>
{/snippet}

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex items-start justify-between gap-3 flex-wrap">
    <div>
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Backups</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1 max-w-2xl">
        Where your fleet backs up, and this server's role as an append-only backup destination.
      </p>
    </div>
    {#if view === 'list' && (data?.configured || nodes.length > 0)}
      <button
        type="button"
        onclick={() => openNew()}
        class="w-full sm:w-auto text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 font-medium shrink-0">
        New repository
      </button>
    {:else if view === 'new'}
      <button type="button" onclick={backToList} class="text-sm px-3 py-2 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 shrink-0">
        ← All repositories
      </button>
    {/if}
  </div>

  {#if error}
    <div class="mt-4 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
  {/if}

  {#if loading && !data}
    <div class="mt-6 h-40 rounded-xl shimmer"></div>
  {:else if view === 'new'}
    {#if !credential}
      <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5 max-w-2xl">
        <h2 class="text-sm font-medium text-zinc-100">New repository</h2>
        <p class="text-xs text-zinc-500 mt-1">A private, append-only namespace one host uploads to, plus the credential it authenticates with.</p>

        {#if nodes.length > 0}
          <div class="mt-4">
            <label for="bt-dest" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Where is it stored?</label>
            <select
              id="bt-dest"
              bind:value={newDestination}
              class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
              {#if data?.configured}
                <option value="server">This server</option>
              {/if}
              {#each nodes as n (n.host_id)}
                <option value={n.host_id} disabled={!n.enrolled}>Node · {n.hostname}{n.enrolled ? '' : ' (coming online…)'}</option>
              {/each}
            </select>
            <p class="mt-1.5 text-[11px] text-zinc-600">Storage nodes receive backups over per-host WireGuard tunnels; nothing is exposed to the internet.</p>
          </div>
        {/if}

        <div class="mt-4">
          <label for="bt-host" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Which host will back up here? <span class="text-zinc-600 normal-case">{newDestination === 'server' ? '(optional)' : '(required for node repositories)'}</span></label>
          <select
            id="bt-host"
            bind:value={newHostId}
            onchange={() => { if (newHostId != null && !newName) { const h = hosts.find((x) => x.id === newHostId); if (h) newName = sanitizeName(h.hostname); } }}
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
            <option value={null}>— none —</option>
            {#each hosts as h (h.id)}
              <option value={h.id}>{h.hostname}</option>
            {/each}
          </select>
          <p class="mt-1.5 text-[11px] text-zinc-600">Links this repository to a monitored host for display. The host still needs the install snippet below to actually back up.</p>
        </div>

        <div class="mt-4">
          <label for="bt-name" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Repository name</label>
          <input
            id="bt-name"
            type="text"
            bind:value={newName}
            autocomplete="off"
            spellcheck="false"
            placeholder="web-01-offsite"
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
          <p class="mt-1.5 text-[11px] text-zinc-600">Letters, digits, dot, dash, underscore. Becomes the repo path and the upload login name.</p>
        </div>

        <div class="mt-4">
          <label for="bt-quota" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Quota <span class="text-zinc-600 normal-case">(GiB, optional)</span></label>
          <input
            id="bt-quota"
            type="number"
            min="0"
            step="1"
            bind:value={newQuotaGiB}
            placeholder="unlimited"
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
          <p class="mt-1.5 text-[11px] text-zinc-600">Uploads are refused once the repo exceeds this. Leave blank for no limit.</p>
        </div>

        {#if createError}
          <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{createError}</div>
        {/if}

        <div class="mt-4 flex justify-end gap-2">
          <button type="button" onclick={backToList} class="text-sm px-3 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40">Cancel</button>
          <button
            type="button"
            disabled={creating || !nameValid}
            onclick={create}
            class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 disabled:cursor-not-allowed font-medium">
            {creating ? 'Creating…' : 'Create repository'}
          </button>
        </div>
      </section>
    {:else}
      <section class="mt-6 space-y-5">
        <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div class="min-w-0">
              <div class="text-xs uppercase tracking-wider text-zinc-500">Upload credential for <span class="font-mono text-zinc-300">{credential.name}</span></div>
              <p class="mt-1 text-[11px] text-amber-300/80">
                Shown once and stored only as a hash. It authenticates uploads only — it cannot decrypt backups (the repo password never leaves the host).
              </p>
            </div>
            <button type="button" onclick={() => credential && copy(credential.password, 'pw')} class="text-xs px-2.5 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 shrink-0">
              {copied === 'pw' ? 'copied' : 'Copy password'}
            </button>
          </div>
          <code class="block mt-3 text-xs font-mono text-zinc-100 break-all bg-zinc-950/60 rounded px-3 py-2 border border-zinc-800">{credential.password}</code>
          <div class="mt-3 flex items-center gap-2">
            <span class="text-[11px] uppercase tracking-wider text-zinc-500 shrink-0">{tunnelActive && !tunnel?.public_http ? 'Repo (via tunnel)' : 'Repo URL'}</span>
            <code class="min-w-0 flex-1 text-xs font-mono text-zinc-300 break-all bg-zinc-950/60 rounded px-3 py-1.5 border border-zinc-800">{repoUrl}</code>
            <button type="button" onclick={() => copy(repoUrl, 'url')} class="text-[11px] px-2 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 shrink-0">{copied === 'url' ? 'copied' : 'copy'}</button>
          </div>
        </div>

        <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
          <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-3">
            <h2 class="text-sm font-medium text-zinc-100">Point a host at it</h2>
            <div class="flex flex-wrap items-center gap-1 text-[11px]">
              {#each [{ id: 'linux', label: 'Linux' }, { id: 'windows', label: 'Windows' }, { id: 'docker', label: 'Docker' }, { id: 'existing', label: 'Existing host' }, { id: 'restic', label: 'Verify with restic' }] as tab (tab.id)}
                <button
                  type="button"
                  onclick={() => (snippetTab = tab.id as SnippetTab)}
                  class="px-2 py-1 rounded-md transition-colors {snippetTab === tab.id ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
                  {tab.label}
                </button>
              {/each}
            </div>
          </header>
          <div class="p-4 sm:p-5 space-y-3 text-sm">
            <p class="text-xs text-zinc-500">
              {#if snippetTab === 'linux'}
                Run on the target host. It provisions the backup timer and writes the credential to a root-owned file. Replace <span class="font-mono">&lt;ADMIN_TOKEN&gt;</span> with your server admin token.
              {:else if snippetTab === 'windows'}
                Run in an elevated PowerShell on the target host. Replace <span class="font-mono">&lt;ADMIN_TOKEN&gt;</span> with your server admin token.
              {:else if snippetTab === 'docker'}
                First add these lines to <span class="font-mono">.env.agent</span>, then run the redeploy command below.
              {:else if snippetTab === 'existing'}
                Already-monitored host with backups enabled? Add this repo to its backup config, then re-run the installer to apply.
              {:else if tunnelActive && !tunnel?.public_http}
                The endpoint only exists inside the tunnel, so verification runs on the enrolled host: <span class="font-mono">sm-agent backup proxy</span> opens the tunnel and prints a local <span class="font-mono">RESTIC_REPOSITORY</span> for the restic CLI. Nothing is executed from this UI.
              {:else}
                Verify connectivity by hand with the restic CLI ({'>='}0.17). Nothing is executed from this UI.
              {/if}
            </p>
            {#if snippetTab === 'existing'}
              <div>
                <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">Add to <span class="font-mono normal-case">/etc/servermonitor-backup/backup.toml</span> <span class="text-zinc-600 normal-case">(root-owned)</span></div>
                <div class="relative">
                  <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{existingToml}</pre>
                  <button type="button" onclick={() => copy(existingToml, 'etoml')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'etoml' ? 'copied' : 'copy'}</button>
                </div>
              </div>
              <div>
                <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">Write <span class="font-mono normal-case">/etc/servermonitor-backup/repo-credentials.env</span> <span class="text-zinc-600 normal-case">(root:sm-agent, mode 0440)</span></div>
                <div class="relative">
                  <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{existingEnv}</pre>
                  <button type="button" onclick={() => copy(existingEnv, 'eenv')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'eenv' ? 'copied' : 'copy'}</button>
                </div>
              </div>
            {:else}
              <div class="relative">
                <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{activeSnippet}</pre>
                <button type="button" onclick={() => copy(activeSnippet, 'snippet')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'snippet' ? 'copied' : 'copy'}</button>
              </div>
              {#if snippetTab === 'docker'}
                <div class="relative">
                  <pre class="text-[11px] font-mono bg-zinc-950 border border-zinc-800 rounded-md p-2.5 overflow-x-auto whitespace-pre text-zinc-300 select-text">{dockerRedeployCommand}</pre>
                  <button type="button" onclick={() => copy(dockerRedeployCommand, 'docker-redeploy')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'docker-redeploy' ? 'copied' : 'copy'}</button>
                </div>
                <p class="text-[11px] text-zinc-500">See <span class="font-mono text-zinc-400">deploy/AGENT-DOCKER.md</span> → Managed backups for the complete recipe.</p>
              {/if}
            {/if}
          </div>
        </div>

        <div class="rounded-xl border {firstBlobSeen ? 'border-emerald-900/40 bg-emerald-950/20' : 'border-zinc-800 bg-zinc-900/40'} p-4 sm:p-5">
          <div class="flex flex-wrap items-center gap-3">
            <span class="h-2 w-2 rounded-full {firstBlobSeen ? 'bg-emerald-400' : 'bg-amber-400'} animate-pulse shrink-0"></span>
            <div class="flex-1 min-w-0">
              {#if firstBlobSeen}
                <div class="text-sm font-medium text-emerald-100">Receiving backups — first data arrived</div>
                <div class="text-xs text-zinc-400 mt-0.5">
                  This repository is live.
                  {#if newHostId != null}
                    Watch it under <a href="/hosts/{newHostId}?tab=backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">{linkedHostname} → Backups</a>.
                  {:else}
                    The host's status appears under its <span class="text-zinc-300">Backups</span> tab.
                  {/if}
                </div>
              {:else}
                <div class="text-sm text-zinc-200">Waiting for the first upload from <span class="font-mono">{credential.name}</span>…</div>
                <div class="text-xs text-zinc-500 mt-0.5">
                  Turns green once the host runs its first backup. After that, its status appears under
                  {#if newHostId != null}
                    <a href="/hosts/{newHostId}?tab=backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">Hosts → {linkedHostname} → Backups</a>.
                  {:else}
                    <span class="text-zinc-300">Hosts → (host) → Backups</span>.
                  {/if}
                </div>
              {/if}
            </div>
            <button type="button" onclick={backToList} class="text-sm px-3 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 shrink-0">Done</button>
          </div>
        </div>
      </section>
    {/if}
  {:else if data}
    <div class="mt-6 space-y-4">
      {#if !data.configured && !tunnelActive}
        {@render howBackupsWork()}

        <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
          <h2 class="text-sm font-medium text-zinc-100">This server isn't a backup destination yet</h2>
          <p class="mt-1 text-xs text-zinc-500 max-w-3xl">
            That's optional — hosts can back up to any restic endpoint. Pick a path:
          </p>
          <div class="mt-4 grid grid-cols-1 md:grid-cols-2 gap-4">
            <div class="rounded-lg border border-zinc-800 bg-zinc-950/40 p-4">
              <div class="text-sm font-medium text-zinc-200">A · Make this server the destination</div>
              <p class="mt-1 text-xs text-zinc-500">Turn this server into an append-only endpoint your fleet pushes to — no separate storage box to run.</p>
              <ol class="mt-3 space-y-1.5 text-xs text-zinc-400 list-decimal list-inside">
                <li>Set one storage backend: <code class="font-mono text-zinc-300">BACKUP_DIR</code> (this server's disk) or <code class="font-mono text-zinc-300">BACKUP_S3_BUCKET</code> (<code class="font-mono text-zinc-300">BACKUP_S3_ENDPOINT</code> for self-hosted S3).</li>
                <li>Ensure TLS — the endpoint uses HTTP Basic, so it refuses plaintext.</li>
                <li>Restart the server, then create a repository here for each host.</li>
              </ol>
            </div>
            <div class="rounded-lg border border-zinc-800 bg-zinc-950/40 p-4">
              <div class="text-sm font-medium text-zinc-200">B · Use an external destination</div>
              <p class="mt-1 text-xs text-zinc-500">Keep destinations off this server — hosts back up straight to a rest-server VPS or S3 / B2. Nothing to enable here.</p>
              <div class="mt-3 text-xs text-zinc-400 space-y-2">
                <p>Install a host with backups pointed at your endpoint:</p>
                <pre class="text-[11px] font-mono bg-zinc-950 border border-zinc-800 rounded-md p-2.5 overflow-x-auto whitespace-pre text-zinc-300 select-text">--enable-backup --backup-repos &lt;rest/s3 url&gt;</pre>
                <p>Then monitor it from its <span class="text-zinc-300">Backups</span> tab.</p>
              </div>
            </div>
          </div>
          <p class="mt-4 text-[11px] text-zinc-600">
            Full setup — rest-server, S3/B2, TLS, and disaster recovery — is in <span class="font-mono text-zinc-500">deploy/BACKUPS.md</span>.
          </p>
        </section>

        {@render fleetSection()}
      {:else}
        {#if tunnelActive && !tunnel?.public_http}
          <div class="rounded-md border border-emerald-900/40 bg-emerald-950/20 px-3 py-2.5 text-xs text-emerald-300/90">
            WireGuard tunnel only — backup destinations are reachable solely through enrolled peers (server UDP port
            <span class="font-mono numeric">{tunnel?.listen_port}</span>). Nothing backup-related is exposed on the web listener, and the tunnels encrypt all backup traffic.
          </div>
        {:else if !data.configured}
          <div class="rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2.5 text-xs text-zinc-400">
            This server has no storage backend of its own (<span class="font-mono">BACKUP_DIR</span> / <span class="font-mono">BACKUP_S3_BUCKET</span>) — repositories live on promoted storage nodes below.
          </div>
        {:else if data.tls.mode === 'insecure'}
          <div class="rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2.5 text-xs text-rose-300">
            No TLS. The backup endpoint authenticates with HTTP Basic, so credentials would travel in plaintext. Set
            <span class="font-mono">TLS_CERT_FILE</span>/<span class="font-mono">TLS_KEY_FILE</span>, put it behind a TLS proxy
            (<span class="font-mono">TRUST_PROXY_TLS=1</span>), or set <span class="font-mono">BACKUP_ACME_DOMAIN</span> for automatic Let's Encrypt.
          </div>
        {:else if data.tls.mode === 'acme'}
          <div class="rounded-md border border-emerald-900/50 bg-emerald-950/25 px-3 py-2.5 text-xs text-emerald-300">
            Automatic TLS (Let's Encrypt) for <span class="font-mono">{data.tls.domain}</span> — a certificate is obtained on the first HTTPS connection.
          </div>
        {:else}
          <div class="rounded-md border border-emerald-900/40 bg-emerald-950/20 px-3 py-2.5 text-xs text-emerald-300/90">
            TLS active ({data.tls.mode === 'proxy' ? 'terminated by a trusted reverse proxy' : 'native certificate'}). Backup traffic is encrypted.
          </div>
        {/if}

        {@render fleetSection()}

        <section>
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div>
              <h2 class="text-sm font-medium text-zinc-100">Repositories</h2>
              <p class="mt-0.5 text-xs text-zinc-500">Each repository is a private, append-only namespace one host uploads to — stored on this server or on a storage node. Create one per host.</p>
            </div>
          </div>

          <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
            <div class="grid grid-cols-1 sm:grid-cols-3 divide-y sm:divide-y-0 sm:divide-x divide-zinc-800">
              <div class="px-4 sm:px-5 py-3 sm:py-4 min-w-0">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Server backend</div>
                <div class="text-lg font-semibold text-zinc-100 mt-1">{data.storage ? (data.storage.kind === 's3' ? 'Object storage' : 'Local disk') : 'None (nodes only)'}</div>
                <div class="text-[11px] text-zinc-500 mt-0.5 font-mono truncate" title={data.storage?.location ?? ''}>{data.storage?.location ?? ''}</div>
              </div>
              <div class="px-4 sm:px-5 py-3 sm:py-4">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Stored</div>
                <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{bytes(data.targets.reduce((s, t) => s + t.used_bytes, 0))}</div>
                <div class="text-[11px] text-zinc-500 mt-0.5">across all repositories</div>
              </div>
              <div class="px-4 sm:px-5 py-3 sm:py-4">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Repositories</div>
                <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{data.targets.filter((t) => !t.revoked_at).length}</div>
                <div class="text-[11px] text-zinc-500 mt-0.5">{data.targets.filter((t) => t.revoked_at).length} revoked</div>
              </div>
            </div>
          </div>

          <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
            {#if data.targets.length === 0}
              <div class="px-4 py-10 text-center text-sm text-zinc-500">
                No repositories yet. Create one, then point a host at it with the generated snippet.
              </div>
            {:else}
              <div class="hidden md:block overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                      <th class="px-4 py-2.5 font-medium">Repository</th>
                      <th class="px-4 py-2.5 font-medium">Destination</th>
                      <th class="px-4 py-2.5 font-medium">Linked host</th>
                      <th class="px-4 py-2.5 font-medium">Used</th>
                      <th class="px-4 py-2.5 font-medium">Measured</th>
                      <th class="px-4 py-2.5 font-medium">Created</th>
                      <th class="px-4 py-2.5 font-medium text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-zinc-800/70">
                    {#each data.targets as t (t.id)}
                      <tr class="hover:bg-zinc-900/60">
                        <td class="px-4 py-3 align-top">
                          <div class="flex items-center gap-2">
                            <span class="h-1.5 w-1.5 rounded-full {t.revoked_at ? 'bg-rose-400' : 'bg-emerald-400'}"></span>
                            <span class="font-mono text-zinc-200">{t.name}</span>
                          </div>
                          {#if t.revoked_at}
                            <span class="ml-3.5 text-[10px] uppercase tracking-wider text-rose-300">revoked</span>
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top text-xs">
                          {#if t.node_host_id}
                            <span class="inline-flex items-center gap-1.5 text-sky-300"><span class="h-1 w-1 rounded-full bg-sky-400"></span>{t.node_hostname || `node ${t.node_host_id}`}</span>
                          {:else}
                            <span class="text-zinc-400">this server</span>
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top text-xs">
                          {#if t.host_id}
                            <a href="/hosts/{t.host_id}?tab=backups" class="text-sky-300 hover:text-sky-200">{t.hostname || `host ${t.host_id}`}</a>
                          {:else}
                            <span class="text-zinc-500">—</span>
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top">
                          <div class="numeric text-zinc-200 text-xs">{bytes(t.used_bytes)}{#if t.quota_bytes}<span class="text-zinc-500"> / {bytes(t.quota_bytes)}</span>{/if}</div>
                          {#if t.quota_bytes}
                            <div class="mt-1.5 h-1.5 w-28 rounded-full bg-zinc-800 overflow-hidden">
                              <div class="h-full rounded-full {barTone(t.used_bytes, t.quota_bytes)}" style="width: {sharePct(t.used_bytes, t.quota_bytes)}%"></div>
                            </div>
                          {:else}
                            <div class="text-[10px] text-zinc-600 mt-0.5">no quota</div>
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top text-zinc-400 text-xs numeric whitespace-nowrap">{t.usage_measured_at ? timeAgo(t.usage_measured_at) : 'never'}</td>
                        <td class="px-4 py-3 align-top text-zinc-400 text-xs numeric whitespace-nowrap">{timeAgo(t.created_at)}</td>
                        <td class="px-4 py-3 align-top">
                          <div class="flex items-center justify-end gap-1.5">
                            {#if !t.node_host_id}
                              <button
                                type="button"
                                onclick={() => measure(t.id)}
                                disabled={measuring === t.id}
                                class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
                                {measuring === t.id ? 'Measuring…' : 'Measure'}
                              </button>
                            {/if}
                            <button
                              type="button"
                              onclick={() => rotate(t.id)}
                              class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                              Rotate
                            </button>
                            {#if t.used_bytes === 0 && !t.node_host_id}
                              <button
                                type="button"
                                onclick={() => (toDelete = t)}
                                title="This repository holds no backups — safe to remove"
                                class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                                Delete
                              </button>
                            {:else if !t.revoked_at}
                              <button
                                type="button"
                                onclick={() => (toRevoke = t)}
                                class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                                Revoke
                              </button>
                            {/if}
                          </div>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
              <div class="md:hidden divide-y divide-zinc-800/70">
                {#each data.targets as t (t.id)}
                  <div class="px-4 py-3 space-y-2">
                    <div class="flex min-w-0 items-center gap-2">
                      <span class="h-1.5 w-1.5 shrink-0 rounded-full {t.revoked_at ? 'bg-rose-400' : 'bg-emerald-400'}"></span>
                      <span class="min-w-0 truncate font-mono text-sm text-zinc-200">{t.name}</span>
                      {#if t.revoked_at}
                        <span class="shrink-0 text-[10px] uppercase tracking-wider text-rose-300">revoked</span>
                      {/if}
                    </div>
                    <div class="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs">
                      {#if t.node_host_id}
                        <span class="inline-flex items-center gap-1.5 text-sky-300"><span class="h-1 w-1 rounded-full bg-sky-400"></span>{t.node_hostname || `node ${t.node_host_id}`}</span>
                      {:else}
                        <span class="text-zinc-400">this server</span>
                      {/if}
                      <span class="text-zinc-700">·</span>
                      {#if t.host_id}
                        <a href="/hosts/{t.host_id}?tab=backups" class="text-sky-300 hover:text-sky-200">{t.hostname || `host ${t.host_id}`}</a>
                      {:else}
                        <span class="text-zinc-600">not linked</span>
                      {/if}
                    </div>
                    <div>
                      <div class="numeric text-xs text-zinc-200">{bytes(t.used_bytes)}{#if t.quota_bytes}<span class="text-zinc-500"> / {bytes(t.quota_bytes)}</span>{/if}</div>
                      {#if t.quota_bytes}
                        <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-zinc-800">
                          <div class="h-full rounded-full {barTone(t.used_bytes, t.quota_bytes)}" style="width: {sharePct(t.used_bytes, t.quota_bytes)}%"></div>
                        </div>
                      {/if}
                    </div>
                    <div class="text-[11px] text-zinc-500 numeric">
                      measured {t.usage_measured_at ? timeAgo(t.usage_measured_at) : 'never'} · created {timeAgo(t.created_at)}
                    </div>
                    <div class="flex justify-end gap-2">
                      {#if !t.node_host_id}
                        <button
                          type="button"
                          onclick={() => measure(t.id)}
                          disabled={measuring === t.id}
                          class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
                          {measuring === t.id ? 'Measuring…' : 'Measure'}
                        </button>
                      {/if}
                      <button
                        type="button"
                        onclick={() => rotate(t.id)}
                        class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                        Rotate
                      </button>
                      {#if t.used_bytes === 0 && !t.node_host_id}
                        <button
                          type="button"
                          onclick={() => (toDelete = t)}
                          title="This repository holds no backups — safe to remove"
                          class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                          Delete
                        </button>
                      {:else if !t.revoked_at}
                        <button
                          type="button"
                          onclick={() => (toRevoke = t)}
                          class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                          Revoke
                        </button>
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>

          <p class="mt-3 max-w-prose text-[11px] text-zinc-600">
            Revoking a credential stops new uploads but never deletes stored backups — this endpoint is append-only, including for admins.
            Remove blobs directly on the storage backend if you need to reclaim space. Empty repositories that never received an upload can be deleted outright.
          </p>
        </section>

        <section>
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div>
              <h2 class="text-sm font-medium text-zinc-100">WireGuard tunnel</h2>
              <p class="mt-0.5 text-xs text-zinc-500">
                {#if tunnelActive}
                  Hosts reach this server's backup endpoint through per-host WireGuard tunnels — no HTTPS exposure needed.
                {:else}
                  Off. Set <code class="font-mono text-zinc-400">BACKUP_WG_PORT</code> (e.g. 51820) on the server to take the backup endpoint off the public listener.
                {/if}
              </p>
            </div>
          </div>

          {#if tunnelActive}
            <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
              <div class="grid grid-cols-1 sm:grid-cols-3 divide-y sm:divide-y-0 sm:divide-x divide-zinc-800">
                <div class="px-4 sm:px-5 py-3 sm:py-4 min-w-0">
                  <div class="text-[11px] uppercase tracking-wider text-zinc-500">Endpoint</div>
                  <div class="text-lg font-semibold text-zinc-100 mt-1 font-mono truncate" title={tunnel?.endpoint}>{tunnel?.endpoint}</div>
                  <div class="text-[11px] text-zinc-500 mt-0.5">UDP · silent to anything but enrolled peers</div>
                </div>
                <div class="px-4 sm:px-5 py-3 sm:py-4">
                  <div class="text-[11px] uppercase tracking-wider text-zinc-500">Tunnel network</div>
                  <div class="text-lg font-semibold text-zinc-100 mt-1 font-mono">{tunnel?.subnet}</div>
                  <div class="text-[11px] text-zinc-500 mt-0.5">server at <span class="font-mono">{tunnel?.server_tunnel_ip}</span></div>
                </div>
                <div class="px-4 sm:px-5 py-3 sm:py-4">
                  <div class="flex items-center justify-between gap-2">
                    <div class="text-[11px] uppercase tracking-wider text-zinc-500">Server public key</div>
                    <button
                      type="button"
                      onclick={() => tunnel?.server_public_key && copy(tunnel.server_public_key, 'wgpub')}
                      class="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-300">{copied === 'wgpub' ? 'copied' : 'copy'}</button>
                  </div>
                  <div class="text-xs font-mono text-zinc-300 mt-2 break-all">{tunnel?.server_public_key}</div>
                </div>
              </div>
            </div>

            <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
              {#if (tunnel?.peers ?? []).length === 0}
                <div class="px-4 py-10 text-center text-sm text-zinc-500">
                  No hosts enrolled yet. Install or reconfigure a host with a <code class="font-mono text-zinc-400">tunnel:</code> repository — the install snippet on a new repository does this automatically.
                </div>
              {:else}
                <div class="hidden md:block overflow-x-auto">
                  <table class="w-full text-sm">
                    <thead>
                      <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                        <th class="px-4 py-2.5 font-medium">Host</th>
                        <th class="px-4 py-2.5 font-medium">Tunnel IP</th>
                        <th class="px-4 py-2.5 font-medium">Last handshake</th>
                        <th class="px-4 py-2.5 font-medium">Traffic</th>
                        <th class="px-4 py-2.5 font-medium">Enrolled</th>
                        <th class="px-4 py-2.5 font-medium text-right">Actions</th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-zinc-800/70">
                      {#each tunnel?.peers ?? [] as p (p.host_id)}
                        <tr class="hover:bg-zinc-900/60">
                          <td class="px-4 py-3">
                            <a href="/hosts/{p.host_id}?tab=backups" class="font-mono text-zinc-200 hover:text-zinc-100">{p.hostname}</a>
                          </td>
                          <td class="px-4 py-3 text-xs font-mono text-zinc-300">{p.tunnel_ip}</td>
                          <td class="px-4 py-3">
                            <div class="flex items-center gap-2 text-xs">
                              <span class="h-1.5 w-1.5 rounded-full {p.connected ? 'bg-emerald-400' : p.last_handshake ? 'bg-zinc-500' : 'bg-zinc-700'}"></span>
                              <span class="{p.connected ? 'text-emerald-300' : 'text-zinc-400'} numeric">
                                {p.last_handshake ? timeAgo(p.last_handshake) : 'never'}
                              </span>
                              {#if p.via}
                                <span class="text-[10px] text-zinc-500">via {p.via}</span>
                              {/if}
                            </div>
                          </td>
                          <td class="px-4 py-3 text-xs text-zinc-400 numeric whitespace-nowrap">↓{bytes(p.rx_bytes)} · ↑{bytes(p.tx_bytes)}</td>
                          <td class="px-4 py-3 text-xs text-zinc-400 numeric whitespace-nowrap">{timeAgo(p.enrolled_at)}</td>
                          <td class="px-4 py-3 text-right">
                            <button
                              type="button"
                              onclick={() => (peerToRevoke = p)}
                              class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                              Revoke peer
                            </button>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
                <div class="md:hidden divide-y divide-zinc-800/70">
                  {#each tunnel?.peers ?? [] as p (p.host_id)}
                    <div class="px-4 py-3 space-y-2">
                      <div class="flex items-center justify-between gap-3">
                        <a href="/hosts/{p.host_id}?tab=backups" class="min-w-0 truncate font-mono text-sm text-zinc-200 hover:text-zinc-100">{p.hostname}</a>
                        <button
                          type="button"
                          onclick={() => (peerToRevoke = p)}
                          class="shrink-0 text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                          Revoke peer
                        </button>
                      </div>
                      <div class="font-mono text-xs text-zinc-300">{p.tunnel_ip}</div>
                      <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                        <span class="h-1.5 w-1.5 rounded-full {p.connected ? 'bg-emerald-400' : p.last_handshake ? 'bg-zinc-500' : 'bg-zinc-700'}"></span>
                        <span class="numeric {p.connected ? 'text-emerald-300' : 'text-zinc-400'}">{p.last_handshake ? timeAgo(p.last_handshake) : 'never'}</span>
                        {#if p.via}<span class="text-zinc-500">via {p.via}</span>{/if}
                      </div>
                      <div class="text-[11px] text-zinc-500 numeric">↓{bytes(p.rx_bytes)} · ↑{bytes(p.tx_bytes)} · enrolled {timeAgo(p.enrolled_at)}</div>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
            <p class="mt-3 max-w-prose text-[11px] text-zinc-600">
              A backup host brings its tunnel up only while a backup, check or restore is running, so an idle handshake age is normal. Handshakes and traffic are measured at whichever endpoint stores the host's backups — this server or a storage node; node-observed stats are relayed by the node's agent about once a minute. Traffic counters reset when the server or the storage node's agent restarts. Revoking a peer removes its tunnel access immediately; its repository credential is revoked separately above.
            </p>
          {/if}
        </section>

        <section>
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div>
              <h2 class="text-sm font-medium text-zinc-100">Storage nodes</h2>
              <p class="mt-0.5 text-xs text-zinc-500">
                Promote a monitored host into a backup destination: its agent serves an append-only restic endpoint inside the tunnel, storing other hosts' encrypted backups on its disk.
              </p>
            </div>
            {#if tunnelActive}
              <button
                type="button"
                onclick={openPromote}
                class="text-sm px-3 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 shrink-0">
                Promote a host
              </button>
            {/if}
          </div>

          {#if demoteError}
            <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{demoteError}</div>
          {/if}

          {#if !tunnelActive}
            <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 px-4 py-6 text-sm text-zinc-500">
              Storage nodes ride the WireGuard control plane — set <code class="font-mono text-zinc-400">BACKUP_WG_PORT</code> on the server first.
            </div>
          {:else}
            {#if promoting}
              <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5 max-w-2xl">
                <h3 class="text-sm font-medium text-zinc-100">Promote a host to storage node</h3>
                <p class="mt-1 text-xs text-zinc-500">
                  No SSH needed: the host's agent picks the role up within a minute, generates its tunnel key, and starts the append-only endpoint under
                  <span class="font-mono text-zinc-400">/var/lib/servermonitor/backup-store</span>.
                </p>
                <div class="mt-4 grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <div>
                    <label for="pn-host" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Host</label>
                    <select
                      id="pn-host"
                      bind:value={promoteHostId}
                      onchange={promoteHostChanged}
                      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
                      <option value={null}>— pick —</option>
                      {#each promotableHosts as h (h.id)}
                        <option value={h.id}>{h.hostname}</option>
                      {/each}
                    </select>
                  </div>
                  <div>
                    <label for="pn-endpoint" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Endpoint other hosts dial</label>
                    <input
                      id="pn-endpoint"
                      type="text"
                      bind:value={promoteEndpoint}
                      placeholder="nas-01.lan or 203.0.113.7"
                      autocomplete="off"
                      spellcheck="false"
                      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
                  </div>
                  <div>
                    <label for="pn-port" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">UDP port</label>
                    <input
                      id="pn-port"
                      type="number"
                      min="1"
                      max="65535"
                      bind:value={promotePort}
                      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
                  </div>
                  <div class="sm:col-span-3">
                    <label for="pn-store-dir" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Store directory <span class="normal-case tracking-normal text-zinc-600">optional</span></label>
                    <input
                      id="pn-store-dir"
                      type="text"
                      bind:value={promoteStoreDir}
                      placeholder="/var/lib/servermonitor/backup-store"
                      autocomplete="off"
                      spellcheck="false"
                      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
                  </div>
                </div>
                <p class="mt-2 text-[11px] text-zinc-600">
                  The endpoint must be reachable over UDP from the hosts that will back up here (LAN name or public address; WireGuard stays silent to strangers).
                </p>
                {#if promoteError}
                  <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{promoteError}</div>
                {/if}
                <div class="mt-4 flex justify-end gap-2">
                  <button type="button" onclick={() => (promoting = false)} class="text-sm px-3 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40">Cancel</button>
                  <button
                    type="button"
                    disabled={promoteBusy || promoteHostId == null}
                    onclick={promote}
                    class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 disabled:cursor-not-allowed font-medium">
                    {promoteBusy ? 'Promoting…' : 'Promote'}
                  </button>
                </div>
              </div>
            {/if}

            {#if editingNode}
              <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5 max-w-2xl">
                <h3 class="text-sm font-medium text-zinc-100">Edit endpoint for <span class="font-mono">{editingNode.hostname}</span></h3>
                <p class="mt-1 text-xs text-zinc-500">
                  Update where backup hosts dial this node. The node's agent applies a new UDP port on its next config poll (within a minute); enrolled hosts pick up the change on their next backup run.
                </p>
                <div class="mt-4 grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <div class="sm:col-span-2">
                    <label for="en-endpoint" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Endpoint other hosts dial</label>
                    <input
                      id="en-endpoint"
                      type="text"
                      bind:value={editEndpoint}
                      placeholder="nas-01.lan or 203.0.113.7"
                      autocomplete="off"
                      spellcheck="false"
                      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
                  </div>
                  <div>
                    <label for="en-port" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">UDP port</label>
                    <input
                      id="en-port"
                      type="number"
                      min="1"
                      max="65535"
                      bind:value={editPort}
                      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
                  </div>
                </div>
                <p class="mt-2 text-[11px] text-zinc-600">
                  Must be reachable over UDP from the hosts that back up here. This only changes the dial address — the node's stored data and repositories are untouched.
                </p>
                {#if editError}
                  <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{editError}</div>
                {/if}
                <div class="mt-4 flex justify-end gap-2">
                  <button type="button" onclick={() => (editingNode = null)} class="text-sm px-3 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40">Cancel</button>
                  <button
                    type="button"
                    disabled={editBusy || !editEndpoint.trim()}
                    onclick={saveEdit}
                    class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 disabled:cursor-not-allowed font-medium">
                    {editBusy ? 'Saving…' : 'Save endpoint'}
                  </button>
                </div>
              </div>
            {/if}

            <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
              {#if nodes.length === 0}
                <div class="px-4 py-10 text-center text-sm text-zinc-500">
                  No storage nodes yet. Promote a connected host to store other hosts' encrypted backups on it.
                </div>
              {:else}
                <div class="hidden md:block overflow-x-auto">
                  <table class="w-full text-sm">
                    <thead>
                      <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                        <th class="px-4 py-2.5 font-medium">Node</th>
                        <th class="px-4 py-2.5 font-medium">Endpoint</th>
                        <th class="px-4 py-2.5 font-medium">Tunnel</th>
                        <th class="px-4 py-2.5 font-medium">Repositories</th>
                        <th class="px-4 py-2.5 font-medium">Stored</th>
                        <th class="px-4 py-2.5 font-medium text-right">Actions</th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-zinc-800/70">
                      {#each nodes as n (n.host_id)}
                        <tr class="hover:bg-zinc-900/60">
                          <td class="px-4 py-3">
                            <a href="/hosts/{n.host_id}" class="font-mono text-zinc-200 hover:text-zinc-100">{n.hostname}</a>
                            {#if n.store_dir && n.store_dir !== '/var/lib/servermonitor/backup-store'}
                              <div class="mt-1 text-[11px] font-mono text-zinc-500">{n.store_dir}</div>
                            {/if}
                          </td>
                          <td class="px-4 py-3 text-xs font-mono text-zinc-300">{n.endpoint}<span class="text-zinc-600">:{n.udp_port}/udp</span></td>
                          <td class="px-4 py-3">
                            <div class="flex items-center gap-2 text-xs">
                              <span class="h-1.5 w-1.5 rounded-full {n.enrolled ? 'bg-emerald-400' : 'bg-amber-400'}"></span>
                              {#if n.enrolled}
                                <span class="text-emerald-300 font-mono">{n.tunnel_ip}</span>
                              {:else}
                                <span class="text-amber-300">coming online…</span>
                              {/if}
                            </div>
                          </td>
                          <td class="px-4 py-3 text-xs text-zinc-300 numeric">{n.target_count}</td>
                          <td class="px-4 py-3 text-xs text-zinc-300 numeric">{bytes(n.used_bytes)}</td>
                          <td class="px-4 py-3 text-right">
                            <div class="flex items-center justify-end gap-1.5">
                              <button
                                type="button"
                                onclick={() => openEdit(n)}
                                class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                                Edit endpoint
                              </button>
                              <button
                                type="button"
                                onclick={() => (nodeToDemote = n)}
                                class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                                Demote
                              </button>
                            </div>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
                <div class="md:hidden divide-y divide-zinc-800/70">
                  {#each nodes as n (n.host_id)}
                    <div class="px-4 py-3 space-y-2">
                      <div class="flex items-center justify-between gap-3">
                        <a href="/hosts/{n.host_id}" class="min-w-0 truncate font-mono text-sm text-zinc-200 hover:text-zinc-100">{n.hostname}</a>
                        <div class="flex shrink-0 items-center gap-2">
                          <button
                            type="button"
                            onclick={() => openEdit(n)}
                            class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                            Edit endpoint
                          </button>
                          <button
                            type="button"
                            onclick={() => (nodeToDemote = n)}
                            class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                            Demote
                          </button>
                        </div>
                      </div>
                      <div class="min-w-0 font-mono text-xs text-zinc-300">
                        <div class="truncate" title={`${n.endpoint}:${n.udp_port}/udp`}>{n.endpoint}<span class="text-zinc-600">:{n.udp_port}/udp</span></div>
                        {#if n.store_dir && n.store_dir !== '/var/lib/servermonitor/backup-store'}
                          <div class="mt-1 truncate text-[11px] text-zinc-500" title={n.store_dir}>{n.store_dir}</div>
                        {/if}
                      </div>
                      <div class="flex items-center gap-2 text-xs">
                        <span class="h-1.5 w-1.5 rounded-full {n.enrolled ? 'bg-emerald-400' : 'bg-amber-400'}"></span>
                        {#if n.enrolled}
                          <span class="font-mono text-emerald-300">{n.tunnel_ip}</span>
                        {:else}
                          <span class="text-amber-300">coming online…</span>
                        {/if}
                      </div>
                      <div class="text-[11px] text-zinc-500 numeric">{n.target_count} {n.target_count === 1 ? 'repository' : 'repositories'} · {bytes(n.used_bytes)} stored</div>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
            <p class="mt-3 max-w-prose text-[11px] text-zinc-600">
              A node stores only ciphertext and never holds repo passwords, so it cannot read or prune what it stores. Demoting requires its repositories to be deleted or revoked first; stored data stays on the node's disk.
            </p>
          {/if}
        </section>
        {@render howBackupsDetails()}
      {/if}
    </div>
  {/if}
</div>

<ConfirmDialog
  open={rotated !== null}
  title="New credential"
  confirmLabel="Done"
  cancelLabel="Close"
  onconfirm={() => { rotated = null; }}
  onclose={() => (rotated = null)}>
  {#snippet body()}
    <div class="space-y-2">
      <p class="text-xs text-amber-300/80">
        The old credential for <span class="font-mono">{rotated?.name}</span> no longer works. Update the host with this new password — shown once.
      </p>
      <code class="block text-xs font-mono text-zinc-100 break-all bg-zinc-950/60 rounded px-3 py-2 border border-zinc-800">{rotated?.password}</code>
      <button type="button" onclick={() => rotated && copy(rotated.password, 'rot')} class="text-[11px] px-2 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200">{copied === 'rot' ? 'copied' : 'Copy password'}</button>
    </div>
  {/snippet}
</ConfirmDialog>

<ConfirmDialog
  open={toRevoke !== null}
  title="Revoke repository credential"
  body={revokeBody}
  confirmLabel="Revoke"
  danger
  onconfirm={doRevoke}
  onclose={() => (toRevoke = null)} />

{#snippet revokeBody()}
  <p class="text-sm text-zinc-300">
    Revoke the credential for <span class="font-mono text-zinc-100">{toRevoke?.name}</span>? Its host can no longer upload.
    Stored backups are kept (append-only) and can still be restored from another credentialed machine.
  </p>
{/snippet}

<ConfirmDialog
  open={toDelete !== null}
  title="Delete repository"
  body={deleteBody}
  confirmLabel="Delete"
  danger
  onconfirm={doDelete}
  onclose={() => (toDelete = null)} />

{#snippet deleteBody()}
  <p class="text-sm text-zinc-300">
    Permanently delete <span class="font-mono text-zinc-100">{toDelete?.name}</span>? It holds no backups, so nothing is
    lost — this just removes the credential and its entry.
  </p>
  <p class="mt-2 text-xs text-zinc-500">
    Only empty repositories can be deleted. If an upload has landed since this list loaded, the delete is refused and you can
    revoke instead.
  </p>
{/snippet}

<ConfirmDialog
  open={peerToRevoke !== null}
  title="Revoke tunnel peer"
  body={revokePeerBody}
  confirmLabel="Revoke peer"
  danger
  onconfirm={revokePeer}
  onclose={() => (peerToRevoke = null)} />

{#snippet revokePeerBody()}
  <p class="text-sm text-zinc-300">
    Remove <span class="font-mono text-zinc-100">{peerToRevoke?.hostname}</span> ({peerToRevoke?.tunnel_ip}) from the tunnel?
    Its backups over the tunnel stop working until it re-enrolls (re-run the installer or
    <span class="font-mono text-zinc-100">sm-agent backup tunnel-enroll</span>).
  </p>
  <p class="mt-2 text-xs text-zinc-500">Stored backups are kept. The repository credential stays valid and can be revoked separately.</p>
{/snippet}

<ConfirmDialog
  open={nodeToDemote !== null}
  title="Demote storage node"
  body={demoteNodeBody}
  confirmLabel="Demote"
  danger
  onconfirm={demoteNode}
  onclose={() => (nodeToDemote = null)} />

{#snippet demoteNodeBody()}
  <p class="text-sm text-zinc-300">
    Demote <span class="font-mono text-zinc-100">{nodeToDemote?.hostname}</span>? Its agent stops the storage endpoint within a minute
    and hosts can no longer back up to it.
  </p>
  <p class="mt-2 text-xs text-zinc-500">
    Refused while the node still has active repositories — revoke or delete them first. Data already stored stays on the node's disk
    under its store directory until you remove it there.
  </p>
{/snippet}
