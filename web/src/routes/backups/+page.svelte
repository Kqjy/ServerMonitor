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
    type BackupDestination,
    type BackupS3CredentialsInput,
    type BackupRepoStatus,
    type Host
  } from '$lib/api';
  import { bytes, statusFor, timeAgo, timeUntil } from '$lib/format';
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

  type DestinationSelection = 'server' | `node:${number}` | `direct:${number}`;
  let newDestination = $state<DestinationSelection>('server');
  let baseUrl = $state('');
  let loading = $state(true);
  let error = $state<string | null>(null);
  let measuring = $state<number | null>(null);

  let newName = $state('');
  let newHostId = $state<number | null>(null);
  let newQuotaGiB = $state<number | null>(null);
  let creating = $state(false);
  let createError = $state<string | null>(null);
  let useScopedCredentials = $state(true);
  let scopedAccessKeyId = $state('');
  let scopedSecretAccessKey = $state('');
  let scopedSessionToken = $state('');
  let credential = $state<BackupCredential | null>(null);
  let snippetTab = $state<SnippetTab>('linux');
  let copied = $state<string | null>(null);

  let rotated = $state<BackupCredential | null>(null);
  let toRevoke = $state<BackupTarget | null>(null);
  let toDelete = $state<BackupTarget | null>(null);
  let retiredNodeTargetToDelete = $state<BackupTarget | null>(null);
  let toEditQuota = $state<BackupTarget | null>(null);
  let editQuotaGiB = $state<number | null>(null);

  let addingDestination = $state(false);
  let destinationName = $state('');
  let destinationEndpoint = $state('');
  let destinationBucket = $state('');
  let destinationRegion = $state('');
  let destinationPrefix = $state('backups/hosts/{host_id}-{hostname}/{repository}');
  let destinationPathStyle = $state(false);
  let destinationAccessKeyId = $state('');
  let destinationSecretAccessKey = $state('');
  let destinationSessionToken = $state('');
  let destinationBusy = $state(false);
  let destinationError = $state<string | null>(null);
  let destinationToDelete = $state<BackupDestination | null>(null);
  let destinationCredentialsFor = $state<BackupDestination | null>(null);
  let repositoryCredentialsFor = $state<BackupTarget | null>(null);
  let repositoryAccessKeyId = $state('');
  let repositorySecretAccessKey = $state('');
  let repositorySessionToken = $state('');
  let repositoryCredentialError = $state<string | null>(null);

  let firstBlobSeen = $state(false);
  let linkedRepoStatus = $state<BackupRepoStatus | null>(null);
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
  const selectedNodeId = $derived(newDestination.startsWith('node:') ? Number(newDestination.slice(5)) : null);
  const selectedDirectId = $derived(newDestination.startsWith('direct:') ? Number(newDestination.slice(7)) : null);
  const selectedNode = $derived(selectedNodeId == null ? null : (nodes.find((n) => n.host_id === selectedNodeId) ?? null));
  const selectedDirectDestination = $derived(selectedDirectId == null ? null : (data?.destinations.find((d) => d.id === selectedDirectId) ?? null));
  const scopedCredentialsValid = $derived(scopedAccessKeyId.trim() !== '' && scopedSecretAccessKey.trim() !== '');
  const destinationReady = $derived(
    newDestination === 'server'
      ? (data?.configured ?? false)
      : selectedNode
        ? selectedNode.enrolled === true && nodeAvailability(selectedNode) === null
        : selectedDirectDestination != null && (useScopedCredentials ? scopedCredentialsValid : selectedDirectDestination.credentials_configured)
  );

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
    const availableNode = nodes.find((n) => n.enrolled && nodeAvailability(n) === null);
    newDestination = data?.configured
      ? 'server'
      : availableNode
        ? `node:${availableNode.host_id}`
        : data?.destinations[0]
          ? `direct:${data.destinations[0].id}`
          : 'server';
    useScopedCredentials = true;
    scopedAccessKeyId = '';
    scopedSecretAccessKey = '';
    scopedSessionToken = '';
    credential = null;
    createError = null;
    firstBlobSeen = false;
    linkedRepoStatus = null;
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
    if (!destinationReady) {
      createError = 'Choose an available storage destination.';
      return;
    }
    if (newDestination !== 'server' && newHostId == null) {
      createError = 'Storage-node and direct S3 repositories require a linked host.';
      return;
    }
    creating = true;
    createError = null;
    try {
      const body: { name: string; host_id?: number; quota_bytes?: number; node_host_id?: number; destination_id?: number; s3_credentials?: BackupS3CredentialsInput } = { name: newName.trim() };
      if (newHostId != null) body.host_id = newHostId;
      if (newQuotaGiB != null && newQuotaGiB > 0) body.quota_bytes = Math.round(newQuotaGiB * 1024 ** 3);
      if (selectedNodeId != null) body.node_host_id = selectedNodeId;
      if (selectedDirectId != null) {
        body.destination_id = selectedDirectId;
        if (useScopedCredentials) {
          body.s3_credentials = {
            access_key_id: scopedAccessKeyId.trim(),
            secret_access_key: scopedSecretAccessKey.trim(),
            ...(scopedSessionToken.trim() ? { session_token: scopedSessionToken.trim() } : {})
          };
        }
      }
      credential = await api.backupTargetCreate(body);
      scopedAccessKeyId = '';
      scopedSecretAccessKey = '';
      scopedSessionToken = '';
      startPoll();
    } catch (e) {
      createError = (e as Error).message;
    } finally {
      creating = false;
    }
  }

  function resetDestinationForm() {
    destinationName = '';
    destinationEndpoint = '';
    destinationBucket = '';
    destinationRegion = '';
    destinationPrefix = 'backups/hosts/{host_id}-{hostname}/{repository}';
    destinationPathStyle = false;
    destinationAccessKeyId = '';
    destinationSecretAccessKey = '';
    destinationSessionToken = '';
    destinationError = null;
  }

  async function createDestination() {
    if (!destinationName.trim() || !destinationBucket.trim()) {
      destinationError = 'Name and bucket are required.';
      return;
    }
    if ((destinationAccessKeyId.trim() === '') !== (destinationSecretAccessKey.trim() === '')) {
      destinationError = 'Access key ID and secret access key must be supplied together.';
      return;
    }
    if (destinationSessionToken.trim() && !destinationAccessKeyId.trim()) {
      destinationError = 'A session token requires an access key ID and secret access key.';
      return;
    }
    destinationBusy = true;
    destinationError = null;
    try {
      await api.backupDestinationCreate({
        name: destinationName.trim(),
        kind: 'direct_s3',
        ...(destinationEndpoint.trim() ? { endpoint: destinationEndpoint.trim() } : {}),
        bucket: destinationBucket.trim(),
        ...(destinationRegion.trim() ? { region: destinationRegion.trim() } : {}),
        prefix_template: destinationPrefix.trim(),
        use_path_style: destinationPathStyle,
        ...(destinationAccessKeyId.trim() ? {
          credentials: {
            access_key_id: destinationAccessKeyId.trim(),
            secret_access_key: destinationSecretAccessKey.trim(),
            ...(destinationSessionToken.trim() ? { session_token: destinationSessionToken.trim() } : {})
          }
        } : {})
      });
      addingDestination = false;
      resetDestinationForm();
      await load();
    } catch (e) {
      destinationError = (e as Error).message;
    } finally {
      destinationBusy = false;
    }
  }

  function openDestinationCredentials(destination: BackupDestination) {
    destinationCredentialsFor = destination;
    destinationAccessKeyId = '';
    destinationSecretAccessKey = '';
    destinationSessionToken = '';
    destinationError = null;
  }

  async function replaceDestinationCredentials() {
    if (!destinationCredentialsFor || !destinationAccessKeyId.trim() || !destinationSecretAccessKey.trim()) {
      destinationError = 'Access key ID and secret access key are required.';
      return;
    }
    destinationBusy = true;
    destinationError = null;
    try {
      await api.backupDestinationSetCredentials(destinationCredentialsFor.id, {
        access_key_id: destinationAccessKeyId.trim(),
        secret_access_key: destinationSecretAccessKey.trim(),
        ...(destinationSessionToken.trim() ? { session_token: destinationSessionToken.trim() } : {})
      });
      destinationCredentialsFor = null;
      resetDestinationForm();
      await load();
    } catch (e) {
      destinationError = (e as Error).message;
    } finally {
      destinationBusy = false;
    }
  }

  async function deleteDestination() {
    if (!destinationToDelete) return;
    await api.backupDestinationDelete(destinationToDelete.id);
    destinationToDelete = null;
    await load();
  }

  function openRepositoryCredentials(repository: BackupTarget) {
    repositoryCredentialsFor = repository;
    repositoryAccessKeyId = '';
    repositorySecretAccessKey = '';
    repositorySessionToken = '';
    repositoryCredentialError = null;
  }

  async function replaceRepositoryCredentials() {
    if (!repositoryCredentialsFor || !repositoryAccessKeyId.trim() || !repositorySecretAccessKey.trim()) {
      throw new Error('Access key ID and secret access key are required.');
    }
    try {
      await api.backupTargetSetCredentials(repositoryCredentialsFor.id, {
        access_key_id: repositoryAccessKeyId.trim(),
        secret_access_key: repositorySecretAccessKey.trim(),
        ...(repositorySessionToken.trim() ? { session_token: repositorySessionToken.trim() } : {})
      });
      repositoryAccessKeyId = '';
      repositorySecretAccessKey = '';
      repositorySessionToken = '';
      repositoryCredentialsFor = null;
      await load();
    } catch (e) {
      repositoryCredentialError = (e as Error).message;
      throw e;
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
      if (newHostId != null && credential) {
        const name = credential.name;
        try {
          const resp = await api.backups(newHostId);
          if (credential?.name === name) {
            linkedRepoStatus = resp.repos.find((r) => r.repo === name)?.status ?? null;
          }
        } catch {}
      }
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

  async function doDeleteRetiredNodeTarget() {
    if (!retiredNodeTargetToDelete) return;
    await api.backupTargetDelete(retiredNodeTargetToDelete.id);
    retiredNodeTargetToDelete = null;
    await load();
  }

  function openQuota(t: BackupTarget) {
    editQuotaGiB = t.quota_bytes ? Math.round((t.quota_bytes / 1024 ** 3) * 100) / 100 : null;
    toEditQuota = t;
  }

  async function saveQuota() {
    if (!toEditQuota) return;
    const gib = editQuotaGiB;
    const quotaBytes = gib != null && gib > 0 ? Math.round(gib * 1024 ** 3) : null;
    const updated = await api.backupTargetSetQuota(toEditQuota.id, quotaBytes);
    if (data) data.targets = data.targets.map((t) => (t.id === updated.id ? updated : t));
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

  type NodeAvailability = 'removed' | 'archived' | 'offline' | null;

  function nodeAvailability(node: BackupNode | null | undefined): NodeAvailability {
    if (!node) return null;
    if (node.host_missing) return 'removed';
    if (node.archived) return 'archived';
    if (statusFor(node.last_seen, node.sample_interval_s ?? 10) === 'bad') return 'offline';
    return null;
  }

  function availabilityText(availability: NodeAvailability): string {
    return availability === 'archived' ? 'text-amber-300' : 'text-rose-300';
  }

  function availabilityDot(availability: NodeAvailability): string {
    return availability === 'archived' ? 'bg-amber-400' : 'bg-rose-400';
  }

  function availabilityBadge(availability: NodeAvailability): string {
    return availability === 'archived'
      ? 'border-amber-500/30 bg-amber-500/10 text-amber-300'
      : 'border-rose-500/30 bg-rose-500/10 text-rose-300';
  }

  function nodeForTarget(target: BackupTarget): BackupNode | null {
    if (target.node_host_id == null) return null;
    return nodes.find((node) => node.host_id === target.node_host_id) ?? null;
  }

  type HostBackup = {
    id: number;
    hostname: string;
    state: string;
    message?: string;
    repos: BackupTarget[];
    revokedRepos: BackupTarget[];
    nodeAvailability: NodeAvailability;
  };

  const reposByHost = $derived.by(() => {
    const m = new Map<number, BackupTarget[]>();
    for (const t of data?.targets ?? []) {
      if (t.host_id == null) continue;
      const arr = m.get(t.host_id) ?? [];
      arr.push(t);
      m.set(t.host_id, arr);
    }
    return m;
  });

  function hostNodeAvailability(repos: BackupTarget[]): NodeAvailability {
    const values = repos.map((repo) => nodeAvailability(nodeForTarget(repo)));
    if (values.includes('removed')) return 'removed';
    if (values.includes('archived')) return 'archived';
    if (values.includes('offline')) return 'offline';
    return null;
  }

  const activeBackupStates = new Set(['ok', 'stale', 'error', 'scheduled', 'stale_agent', 'agent_perms']);
  const hostBackups = $derived.by<HostBackup[]>(() =>
    hosts
      .filter((h) => {
        const state = h.collector_status?.backup?.state ?? '';
        return activeBackupStates.has(state) && !((state === 'stale_agent' || state === 'agent_perms') && nodes.some((n) => n.host_id === h.id));
      })
      .map((h) => {
        const targets = reposByHost.get(h.id) ?? [];
        const repos = targets.filter((target) => !target.revoked_at);
        return {
          id: h.id,
          hostname: h.hostname,
          state: h.collector_status!.backup.state,
          message: h.collector_status!.backup.message,
          repos,
          revokedRepos: targets.filter((target) => target.revoked_at),
          nodeAvailability: hostNodeAvailability(repos)
        };
      })
      .sort((a, b) => a.hostname.localeCompare(b.hostname))
  );

  function stateDot(state: string): string {
    switch (state) {
      case 'ok':
        return 'bg-emerald-400';
      case 'stale':
      case 'stale_agent':
        return 'bg-amber-400';
      case 'agent_perms':
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
      case 'agent_perms':
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
      case 'agent_perms':
        return 'backups blocked';
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
      "SM_ADMIN_TOKEN='<ADMIN_TOKEN>'",
      "SM_ENABLE_BACKUP='1'",
      `SM_BACKUP_REPOS='${repoSpec}'`,
      `SM_BACKUP_REPO_NAMES='${credential.name}'`,
      `SM_BACKUP_REST_USERNAME='${credential.name}'`,
      `SM_BACKUP_REST_PASSWORD='${credential.password ?? ''}'`,
      "SM_BACKUP_PRUNE_MODE='external'"
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
      `$env:SM_BACKUP_REST_PASSWORD="${credential.password ?? ''}"`,
      '$env:SM_BACKUP_PRUNE_MODE="external"',
      `iex (iwr -useb ${baseUrl}/install.ps1).Content`
    ].join('\n');
  });

  const dockerSnippet = $derived.by(() => {
    if (!credential) return '';
    return [
      'SM_ENABLE_BACKUP=1',
      `SM_BACKUP_REPOS="${repoSpec}"`,
      `SM_BACKUP_REPO_NAMES="${credential.name}"`,
      `SM_BACKUP_REST_USERNAME="${credential.name}"`,
      `SM_BACKUP_REST_PASSWORD="${credential.password ?? ''}"`,
      'SM_BACKUP_PRUNE_MODE=external',
      'SM_BACKUP_TIME=02:30',
      'TZ=UTC'
    ].join('\n');
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
RESTIC_REST_PASSWORD=${credential.password ?? ''}`;
  });

  const resticSnippet = $derived.by(() => {
    if (!credential) return '';
    if (selectedNode || (tunnelActive && !tunnel?.public_http)) {
      return `export RESTIC_REST_USERNAME='${credential.name}'
export RESTIC_REST_PASSWORD='${credential.password ?? ''}'
sm-agent backup proxy --config /etc/servermonitor-backup/backup.toml
restic -r <printed RESTIC_REPOSITORY> snapshots`;
    }
    return `export RESTIC_REST_USERNAME='${credential.name}'
export RESTIC_REST_PASSWORD='${credential.password ?? ''}'
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

  const runNowCommand = $derived(
    snippetTab === 'windows'
      ? "Start-ScheduledTask 'ServerMonitor Backup'"
      : snippetTab === 'docker'
        ? 'docker compose -f deploy/docker-compose.agent.yml exec sm-agent /usr/local/bin/sm-agent backup run'
        : 'sudo systemctl start sm-backup.service'
  );

  function absTime(iso?: string): string {
    if (!iso) return '';
    const d = new Date(iso);
    return isNaN(d.getTime()) ? '' : d.toLocaleString();
  }

  const wizardRepoCreatedAt = $derived.by(() => {
    if (!credential) return null;
    const id = credential.id;
    return data?.targets.find((x) => x.id === id)?.created_at ?? null;
  });

  function sinceCreation(iso?: string): boolean {
    if (!iso) return false;
    if (!wizardRepoCreatedAt) return true;
    return new Date(iso).getTime() >= new Date(wizardRepoCreatedAt).getTime();
  }

  type WizardStage = 'waiting' | 'scheduled' | 'running' | 'failed' | 'uploaded' | 'received';
  const wizardStage = $derived.by<WizardStage>(() => {
    if (firstBlobSeen) return 'received';
    if (newHostId != null && credential) {
      const entry = live[newHostId]?.[credential.name];
      if (entry && entry.running && liveNow - entry.lastSeen < 30_000) return 'running';
    }
    if (linkedRepoStatus) {
      const s = linkedRepoStatus;
      if (!s.success && s.error && sinceCreation(s.last_finished)) return 'failed';
      if ((s.success || s.last_success) && sinceCreation(s.last_success ?? s.last_finished)) return 'uploaded';
      return 'scheduled';
    }
    return 'waiting';
  });

  onMount(async () => {
    await load();
    hostsTimer = setInterval(() => {
      void loadHosts();
      void api
        .backupNodes()
        .then((resp) => (nodes = resp?.nodes ?? nodes))
        .catch(() => {});
    }, 30_000);
    liveUnsub = subscribeHosts(['backup_running'], receiveLive);
    liveTimer = setInterval(() => (liveNow = Date.now()), 5_000);
    const sp = $page.url.searchParams;
    if (sp.get('new') === '1' && (data?.configured || nodes.length > 0 || (data?.destinations.length ?? 0) > 0)) {
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
  <p class="mt-1.5 text-xs text-zinc-400 leading-relaxed">
    Monitored hosts back up their own files with restic — encrypted on the host, so every backend sees ciphertext only.
    Storage backend, repository namespace, and network/data path are independent choices. Enable a host's backup runtime at install with
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
      <div class="text-zinc-200 font-medium">Effective data paths</div>
      <div class="mt-1 space-y-0.5 text-[11px] text-zinc-400">
        <div>Agent → Server disk</div>
        <div>Agent → ServerMonitor → S3</div>
        <div>Agent → storage node</div>
        <div class="text-emerald-300">Agent → S3 directly</div>
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

{#snippet hostRepositories(h: HostBackup)}
  {#if h.repos.length > 0}
    {#each h.repos as repo, index (repo.id)}
      {@const availability = nodeAvailability(nodeForTarget(repo))}
      {#if index > 0}<span class="text-zinc-600">, </span>{/if}
      <span class="font-mono text-emerald-300/90">{repo.name}</span><span class="text-zinc-600">{' · '}{repo.data_path.label}</span>{#if availability}<span class={availabilityText(availability)}>{' · '}node {availability}</span>{/if}
    {/each}
  {:else if h.revokedRepos.length > 0}
    {#each h.revokedRepos as repo, index (repo.id)}
      {#if index > 0}<span class="text-zinc-600">, </span>{/if}
      <span class="font-mono text-zinc-500">{repo.name}</span><span class="text-rose-300">{' · '}revoked</span>
    {/each}
  {:else}
    <span class="text-zinc-600">external / not linked</span>
  {/if}
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
                {@const destinationUnavailable = h.nodeAvailability}
                <tr class="hover:bg-zinc-900/60">
                  <td class="px-4 py-3 whitespace-nowrap">
                    <a href="/hosts/{h.id}?tab=backups" class="font-mono text-zinc-200 hover:text-zinc-100">{h.hostname}</a>
                  </td>
                  <td class="px-4 py-3">
                    <div class="flex items-center gap-2">
                      <span class="h-1.5 w-1.5 rounded-full {running ? 'bg-emerald-400 animate-pulse' : destinationUnavailable ? availabilityDot(destinationUnavailable) : stateDot(h.state)}"></span>
                      <span class="text-xs {running ? 'text-emerald-300' : destinationUnavailable ? availabilityText(destinationUnavailable) : stateText(h.state)}">{running ? 'backing up now' : destinationUnavailable ? `node ${destinationUnavailable}` : stateLabel(h.state)}</span>
                    </div>
                    {#if h.message}<div class="mt-0.5 text-[11px] text-zinc-600 truncate max-w-xs" title={h.message}>{h.message}</div>{/if}
                  </td>
                  <td class="px-4 py-3 text-xs">
                    {@render hostRepositories(h)}
                  </td>
                  <td class="px-4 py-3 text-right whitespace-nowrap">
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
            {@const destinationUnavailable = h.nodeAvailability}
            <div class="px-4 py-3 space-y-2">
              <div class="flex items-center justify-between gap-3">
                <a href="/hosts/{h.id}?tab=backups" class="min-w-0 truncate font-mono text-sm text-zinc-200 hover:text-zinc-100">{h.hostname}</a>
                <a href="/hosts/{h.id}?tab=backups" class="shrink-0 text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Backups tab</a>
              </div>
              <div>
                <div class="flex items-center gap-2">
                  <span class="h-1.5 w-1.5 rounded-full {running ? 'bg-emerald-400 animate-pulse' : destinationUnavailable ? availabilityDot(destinationUnavailable) : stateDot(h.state)}"></span>
                  <span class="text-xs {running ? 'text-emerald-300' : destinationUnavailable ? availabilityText(destinationUnavailable) : stateText(h.state)}">{running ? 'backing up now' : destinationUnavailable ? `node ${destinationUnavailable}` : stateLabel(h.state)}</span>
                </div>
                {#if h.message}<div class="mt-0.5 text-[11px] text-zinc-600 truncate" title={h.message}>{h.message}</div>{/if}
              </div>
              <div class="text-xs">
                {@render hostRepositories(h)}
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </section>
{/snippet}

{#snippet directDestinationsSection()}
  <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
    <header class="flex flex-wrap items-start justify-between gap-3 border-b border-zinc-800 px-4 sm:px-5 py-4">
      <div>
        <h2 class="text-sm font-medium text-zinc-100">Direct external S3 destinations</h2>
        <p class="mt-0.5 text-xs text-zinc-500">Reusable backend definitions. Repository namespaces and host assignments are created separately below.</p>
      </div>
      <button type="button" onclick={() => { resetDestinationForm(); destinationCredentialsFor = null; addingDestination = true; }} class="text-xs px-3 py-1.5 rounded-md border border-emerald-500/40 bg-emerald-500/10 text-emerald-200 hover:bg-emerald-500/20">Add S3 destination</button>
    </header>

    {#if addingDestination || destinationCredentialsFor}
      <div class="border-b border-zinc-800 bg-zinc-950/30 p-4 sm:p-5">
        <h3 class="text-sm font-medium text-zinc-200">{destinationCredentialsFor ? `Replace credentials · ${destinationCredentialsFor.name}` : 'New direct S3 destination'}</h3>
        {#if !destinationCredentialsFor}
          <div class="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label for="dest-name" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Destination name</label>
              <input id="dest-name" bind:value={destinationName} placeholder="Cloudflare R2" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm" />
            </div>
            <div>
              <label for="dest-bucket" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Bucket</label>
              <input id="dest-bucket" bind:value={destinationBucket} placeholder="servermonitor-backups" autocomplete="off" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
            </div>
            <div class="sm:col-span-2">
              <label for="dest-endpoint" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">S3 endpoint <span class="normal-case text-zinc-600">(blank for AWS)</span></label>
              <input id="dest-endpoint" bind:value={destinationEndpoint} placeholder="https://ACCOUNT_ID.r2.cloudflarestorage.com" autocomplete="off" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
              <p class="mt-1 text-[10px] text-zinc-600">Custom endpoints must be an HTTPS origin without credentials or a path.</p>
            </div>
            <div>
              <label for="dest-region" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Region</label>
              <input id="dest-region" bind:value={destinationRegion} placeholder="auto (R2) or us-east-1" autocomplete="off" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
            </div>
            <label class="flex items-center gap-2 self-end rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 text-xs text-zinc-300">
              <input type="checkbox" bind:checked={destinationPathStyle} class="accent-emerald-500" /> Path-style addressing
            </label>
            <div class="sm:col-span-2">
              <label for="dest-prefix" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Per-host prefix template</label>
              <input id="dest-prefix" bind:value={destinationPrefix} autocomplete="off" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
              <p class="mt-1 text-[11px] text-zinc-600">Must contain <span class="font-mono">{'{host_id}'}</span> and <span class="font-mono">{'{repository}'}</span>; <span class="font-mono">{'{hostname}'}</span> is optional.</p>
            </div>
          </div>
        {/if}

        <div class="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="dest-key" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Access key ID</label>
            <input id="dest-key" type="password" bind:value={destinationAccessKeyId} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
          <div>
            <label for="dest-secret" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Secret access key</label>
            <input id="dest-secret" type="password" bind:value={destinationSecretAccessKey} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
          <div class="sm:col-span-2">
            <label for="dest-token" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Session token <span class="normal-case text-zinc-600">(optional)</span></label>
            <input id="dest-token" type="password" bind:value={destinationSessionToken} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
          </div>
        </div>
        <p class="mt-2 text-[11px] text-zinc-500">Shared credentials are optional. Secrets are write-only in the admin API/UI and encrypted in PostgreSQL with <span class="font-mono">BACKUP_SECRETS_KEY</span>. Leave them blank when every repository will use its own prefix-scoped credential.</p>
        {#if destinationError}<div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{destinationError}</div>{/if}
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" onclick={() => { addingDestination = false; destinationCredentialsFor = null; resetDestinationForm(); }} class="text-sm px-3 py-1.5 text-zinc-400 hover:text-zinc-200">Cancel</button>
          <button type="button" disabled={destinationBusy} onclick={destinationCredentialsFor ? replaceDestinationCredentials : createDestination} class="text-sm px-4 py-1.5 rounded-md border border-emerald-500/40 bg-emerald-500/10 text-emerald-200 disabled:opacity-50">{destinationBusy ? 'Saving…' : destinationCredentialsFor ? 'Replace credentials' : 'Create destination'}</button>
        </div>
      </div>
    {/if}

    {#if (data?.destinations.length ?? 0) === 0}
      <div class="px-4 py-7 text-center text-sm text-zinc-500">No centrally managed direct destinations yet.</div>
    {:else}
      <div class="divide-y divide-zinc-800/70">
        {#each data?.destinations ?? [] as destination (destination.id)}
          <div class="flex flex-wrap items-center justify-between gap-3 px-4 sm:px-5 py-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2"><span class="h-1.5 w-1.5 rounded-full bg-emerald-400"></span><span class="font-medium text-zinc-200">{destination.name}</span><span class="rounded border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-emerald-300">Agent → S3 directly</span></div>
              <div class="mt-1 truncate font-mono text-[11px] text-zinc-500" title={`${destination.endpoint || 'AWS S3'}/${destination.bucket}`}>{destination.endpoint || 'AWS S3'} / {destination.bucket}</div>
              <div class="mt-0.5 text-[11px] text-zinc-600">{destination.repository_count} {destination.repository_count === 1 ? 'namespace' : 'namespaces'} · credentials {destination.credentials_configured ? 'stored' : 'required per repository'}</div>
            </div>
            <div class="flex items-center gap-2">
              <button type="button" onclick={() => openDestinationCredentials(destination)} class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Replace credentials</button>
              {#if destination.repository_count === 0}
                <button type="button" onclick={() => (destinationToDelete = destination)} class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Delete</button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>
{/snippet}

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex items-start justify-between gap-3 flex-wrap">
    <div>
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Backups</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1">
        Storage backends, repository namespaces, and the exact data path for every fleet backup.
      </p>
    </div>
    {#if view === 'list' && (data?.configured || nodes.length > 0 || (data?.destinations.length ?? 0) > 0)}
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
        <p class="text-xs text-zinc-500 mt-1">Choose the storage backend, define a repository namespace, then verify the exact network/data path separately.</p>

          <div class="mt-4">
            <label for="bt-dest" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Storage destination</label>
            <select
              id="bt-dest"
              bind:value={newDestination}
              class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
              {#if data?.configured}
                <option value="server">ServerMonitor backend · {data.storage?.kind === 's3' ? 'S3 gateway' : 'server disk'}</option>
              {/if}
              {#each nodes as n (n.host_id)}
                {@const availability = nodeAvailability(n)}
                <option value={`node:${n.host_id}`} disabled={!n.enrolled || availability !== null}>Storage node · {n.hostname || `node ${n.host_id}`}{availability === 'removed' ? ' (host removed)' : availability === 'archived' ? ' (host archived)' : availability === 'offline' ? ' (offline)' : n.node_state === 'error' ? ' (endpoint failing)' : n.enrolled ? '' : ' (coming online…)'}</option>
              {/each}
              {#each data?.destinations ?? [] as destination (destination.id)}
                <option value={`direct:${destination.id}`}>Direct external S3 · {destination.name}</option>
              {/each}
            </select>
            <div class="mt-2 rounded-md border border-zinc-800 bg-zinc-950/50 px-3 py-2 text-xs">
              <div class="text-[10px] uppercase tracking-wider text-zinc-600">Effective data path</div>
              {#if selectedDirectDestination}
                <div class="mt-1 font-medium text-emerald-300">Agent → S3 directly</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">S3 API to {selectedDirectDestination.endpoint || 'AWS S3'} · ServerMonitor delivers configuration only</div>
              {:else if selectedNode}
                <div class="mt-1 font-medium text-sky-300">Agent → storage node</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">Public transport: WireGuard UDP to {selectedNode.endpoint}:{selectedNode.udp_port}</div>
              {:else if data?.storage?.kind === 's3'}
                <div class="mt-1 font-medium text-amber-300">Agent → ServerMonitor → S3</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">ServerMonitor terminates restic, temporarily spools each object, then relays it to S3</div>
              {:else}
                <div class="mt-1 font-medium text-zinc-300">Agent → Server disk</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">The server's append-only restic endpoint writes to its local backup directory</div>
              {/if}
            </div>
          </div>

        <div class="mt-4">
          <label for="bt-host" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Which host owns this namespace? <span class="text-zinc-600 normal-case">{newDestination === 'server' ? '(optional for legacy gateway repositories)' : '(required)'}</span></label>
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
          <p class="mt-1.5 text-[11px] text-zinc-600">Direct destinations are delivered only to this authenticated agent and expand to a unique per-host object prefix.</p>
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
          <p class="mt-1.5 text-[11px] text-zinc-600">Letters, digits, dot, dash, underscore. This is the logical namespace; the storage prefix is shown separately.</p>
        </div>

        {#if selectedDirectDestination}
          <div class="mt-4 rounded-lg border border-zinc-800 bg-zinc-950/40 p-3">
            <label class="flex items-start gap-2 text-xs text-zinc-300">
              <input type="checkbox" bind:checked={useScopedCredentials} class="mt-0.5 accent-emerald-500" />
              <span><span class="font-medium">Use repository-scoped credentials</span> <span class="text-emerald-400/80">recommended</span><span class="mt-0.5 block text-[11px] text-zinc-600">Use IAM/R2 credentials restricted to this expanded prefix. Uncheck to reuse the destination credential.</span></span>
            </label>
            {#if useScopedCredentials}
              <div class="mt-3 grid grid-cols-1 sm:grid-cols-2 gap-3">
                <div>
                  <label for="scoped-key" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Access key ID</label>
                  <input id="scoped-key" type="password" bind:value={scopedAccessKeyId} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
                </div>
                <div>
                  <label for="scoped-secret" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Secret access key</label>
                  <input id="scoped-secret" type="password" bind:value={scopedSecretAccessKey} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
                </div>
                <div class="sm:col-span-2">
                  <label for="scoped-session" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Session token <span class="normal-case text-zinc-600">(optional)</span></label>
                  <input id="scoped-session" type="password" bind:value={scopedSessionToken} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
                </div>
              </div>
            {:else if !selectedDirectDestination.credentials_configured}
              <p class="mt-3 text-xs text-rose-300">This destination has no shared credential. Supply scoped credentials to continue.</p>
            {/if}
          </div>
        {/if}

        {#if !selectedDirectDestination}
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
        {/if}

        {#if createError}
          <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{createError}</div>
        {/if}

        <div class="mt-4 flex justify-end gap-2">
          <button type="button" onclick={backToList} class="text-sm px-3 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40">Cancel</button>
          <button
            type="button"
            disabled={creating || !nameValid || !destinationReady}
            onclick={create}
            class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 disabled:cursor-not-allowed font-medium">
            {creating ? 'Creating…' : 'Create repository'}
          </button>
        </div>
      </section>
    {:else}
      <section class="mt-6 space-y-5">
        {#if credential.managed}
          <div class="rounded-xl border border-emerald-900/40 bg-emerald-950/20 p-4 sm:p-5">
            <div class="text-xs uppercase tracking-wider text-emerald-400/80">Centrally managed direct repository</div>
            <h2 class="mt-1 text-base font-medium text-emerald-100">{credential.name} is assigned to {linkedHostname || 'the selected host'}</h2>
            <div class="mt-4 grid grid-cols-1 sm:grid-cols-3 gap-3 text-xs">
              <div class="rounded-lg border border-zinc-800 bg-zinc-950/50 p-3">
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Storage backend</div>
                <div class="mt-1 text-zinc-200">{selectedDirectDestination?.name ?? 'External S3'}</div>
                <div class="mt-0.5 font-mono text-[11px] text-zinc-500">{selectedDirectDestination?.bucket}</div>
              </div>
              <div class="rounded-lg border border-zinc-800 bg-zinc-950/50 p-3">
                <div class="text-[10px] uppercase tracking-wider text-zinc-500">Repository namespace</div>
                <div class="mt-1 font-mono text-zinc-200">{credential.name}</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">expanded to a unique per-host prefix</div>
              </div>
              <div class="rounded-lg border border-emerald-900/40 bg-emerald-950/20 p-3">
                <div class="text-[10px] uppercase tracking-wider text-emerald-500/80">Effective data path</div>
                <div class="mt-1 font-medium text-emerald-200">Agent → S3 directly</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">ServerMonitor is control plane only</div>
              </div>
            </div>
            <p class="mt-4 text-xs leading-relaxed text-zinc-400">
              No secret is shown here. ServerMonitor stores the S3 credential encrypted, and the linked agent receives only its assignment in an agent-token-bound AES-GCM envelope over HTTPS. Backup objects never spool on or pass through this server.
            </p>
          </div>

          <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
            <h2 class="text-sm font-medium text-zinc-100">What happens next</h2>
            <ol class="mt-3 list-decimal space-y-2 pl-5 text-xs text-zinc-400">
              <li>The resident agent receives this repository within one minute and writes credentials to its private managed-backup directory.</li>
              <li>If backups are already enabled on the host, the next scheduled run includes this repository automatically—no installer rerun or per-agent S3 env change.</li>
              <li>If this host has never had backups enabled, wait for the assignment to sync, then enable the backup runtime once with <code class="font-mono text-zinc-300">SM_ENABLE_BACKUP=1</code> and <code class="font-mono text-zinc-300">SM_BACKUP_PRUNE_MODE=external</code>. Do not set <code class="font-mono text-zinc-300">SM_BACKUP_REPOS</code> or S3 secrets.</li>
              <li>After the assignment arrives, run <code class="font-mono text-zinc-300">sm-agent backup recovery-kit</code> and save the refreshed break-glass kit offline.</li>
            </ol>
            <div class="mt-4 flex flex-wrap items-center justify-between gap-3">
              {#if newHostId != null}
                <a href="/hosts/{newHostId}?tab=backups" class="text-xs text-sky-300 hover:text-sky-200">Open {linkedHostname || 'host'} backups →</a>
              {/if}
              <button type="button" onclick={backToList} class="text-sm px-3 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Done</button>
            </div>
          </div>
        {:else}
        <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div class="min-w-0">
              <div class="text-xs uppercase tracking-wider text-zinc-500">Upload credential for <span class="font-mono text-zinc-300">{credential.name}</span></div>
              <p class="mt-1 text-[11px] text-amber-300/80">
                Shown once and stored only as a hash. It authenticates uploads only — it cannot decrypt backups (the repo password never leaves the host).
              </p>
            </div>
            <button type="button" onclick={() => credential && copy(credential.password ?? '', 'pw')} class="text-xs px-2.5 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 shrink-0">
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
                Add all of these lines to <span class="font-mono">.env.agent</span>, then run the redeploy command below. This is the complete set for this repository — the two
                <span class="font-mono">SM_BACKUP_REST_*</span> lines are the upload credential above and are required even for <span class="font-mono">tunnel:</span> repositories.
                The encryption key is generated inside the agent's state volume, so there is nothing else to configure.
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
                <p class="text-[11px] text-zinc-500">
                  Backs up <span class="font-mono">/etc /home /root /var/lib</span> by default, plus <span class="font-mono">/var/lib/docker/volumes</span> when present. Docker image layers, build caches and container logs are excluded; named volumes are included. Override with <span class="font-mono">SM_BACKUP_PATHS</span> / <span class="font-mono">SM_BACKUP_EXCLUDES</span>; the daily run happens at
                  <span class="font-mono">SM_BACKUP_TIME</span> interpreted in <span class="font-mono">TZ</span>. See <span class="font-mono text-zinc-400">deploy/AGENT-DOCKER.md</span> → Managed backups
                  for the complete recipe, including the one-time recovery kit that protects the generated encryption key.
                </p>
              {/if}
            {/if}
          </div>
        </div>

        {#snippet runNowBlock()}
          <div class="relative mt-2">
            <pre class="text-[11px] font-mono bg-zinc-950 border border-zinc-800 rounded-md p-2.5 pr-16 overflow-x-auto whitespace-pre text-zinc-300 select-text">{runNowCommand}</pre>
            <button type="button" onclick={() => copy(runNowCommand, 'runnow')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'runnow' ? 'copied' : 'copy'}</button>
          </div>
        {/snippet}
        {#snippet hostBackupsLink()}
          {#if newHostId != null}
            <a href="/hosts/{newHostId}?tab=backups" class="text-sky-300 hover:text-sky-200 underline underline-offset-2">{linkedHostname} → Backups</a>
          {:else}
            <span class="text-zinc-300">Hosts → (host) → Backups</span>
          {/if}
        {/snippet}
        <div class="rounded-xl border {wizardStage === 'received' || wizardStage === 'uploaded' ? 'border-emerald-900/40 bg-emerald-950/20' : wizardStage === 'failed' ? 'border-rose-900/50 bg-rose-950/20' : 'border-zinc-800 bg-zinc-900/40'} p-4 sm:p-5">
          <div class="flex flex-wrap items-start gap-3">
            <span class="mt-1.5 h-2 w-2 rounded-full shrink-0 {wizardStage === 'received' || wizardStage === 'uploaded' ? 'bg-emerald-400' : wizardStage === 'running' ? 'bg-emerald-400 animate-pulse' : wizardStage === 'failed' ? 'bg-rose-400' : wizardStage === 'scheduled' ? 'bg-sky-400 animate-pulse' : 'bg-amber-400 animate-pulse'}"></span>
            <div class="flex-1 min-w-0">
              {#if wizardStage === 'received'}
                <div class="text-sm font-medium text-emerald-100">Receiving backups — first data arrived</div>
                <div class="text-xs text-zinc-400 mt-0.5">
                  This repository is live. Watch it under {@render hostBackupsLink()}.
                </div>
              {:else if wizardStage === 'uploaded'}
                <div class="text-sm font-medium text-emerald-100">First backup completed</div>
                <div class="text-xs text-zinc-400 mt-0.5">
                  <span class="font-mono">{credential.name}</span> received its first snapshot — the stored size updates within a minute. Watch it under {@render hostBackupsLink()}.
                </div>
              {:else if wizardStage === 'running'}
                <div class="text-sm font-medium text-emerald-100">Backing up now</div>
                <div class="text-xs text-zinc-400 mt-0.5">
                  {linkedHostname || 'The host'} is uploading to <span class="font-mono">{credential.name}</span> — this turns green as soon as the first data lands.
                </div>
              {:else if wizardStage === 'failed'}
                <div class="text-sm font-medium text-rose-200">First backup failed{#if linkedHostname}&nbsp;on <span class="font-mono">{linkedHostname}</span>{/if}</div>
                {#if linkedRepoStatus?.error}
                  <div class="text-xs text-rose-300/90 mt-0.5 break-words font-mono">{linkedRepoStatus.error}</div>
                {/if}
                <div class="text-xs text-zinc-500 mt-1">
                  The usual cause is a missing line from the snippet above — both <span class="font-mono">SM_BACKUP_REST_*</span> credential lines must be present. Fix the config, apply it, then run again:
                </div>
                {@render runNowBlock()}
              {:else if wizardStage === 'scheduled'}
                <div class="text-sm font-medium text-sky-200">{linkedHostname || 'Host'} is configured — first backup scheduled</div>
                <div class="text-xs text-zinc-400 mt-0.5">
                  {#if linkedRepoStatus?.next_run}
                    Next run <span class="numeric text-zinc-200">{absTime(linkedRepoStatus.next_run)}</span> <span class="text-zinc-500">({timeUntil(linkedRepoStatus.next_run)})</span>.
                  {:else}
                    The host picked up the configuration and will back up at its scheduled time.
                  {/if}
                  Turns green when the first data arrives — or run one now on the host:
                </div>
                {@render runNowBlock()}
              {:else}
                <div class="text-sm text-zinc-200">Waiting for the first upload from <span class="font-mono">{credential.name}</span>…</div>
                <div class="text-xs text-zinc-500 mt-0.5">
                  {#if newHostId != null}
                    A host applies the install or redeploy within a minute, and its schedule then shows up here.
                  {:else}
                    A host applies the install or redeploy within a minute.
                  {/if}
                  Backups run at the scheduled time (<span class="font-mono">02:30</span> in the snippets), so a freshly configured host stays in this state until its first run — or trigger one now on the host:
                </div>
                {@render runNowBlock()}
                <div class="text-xs text-zinc-500 mt-2">
                  Turns green once the first backup lands; after that, status lives under {@render hostBackupsLink()}.
                </div>
              {/if}
            </div>
            <button type="button" onclick={backToList} class="text-sm px-3 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 shrink-0">Done</button>
          </div>
        </div>
        {/if}
      </section>
    {/if}
  {:else if data}
    <div class="mt-6 space-y-4">
      {#if !data.configured && !tunnelActive && data.targets.length === 0 && nodes.length === 0 && data.destinations.length === 0}
        {@render howBackupsWork()}

        {@render directDestinationsSection()}

        <section class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
          <h2 class="text-sm font-medium text-zinc-100">This server isn't a backup destination yet</h2>
          <p class="mt-1 text-xs text-zinc-500">
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
              <p class="mt-1 text-xs text-zinc-500">Keep the data plane off this server. Add a direct S3/R2 destination above; ServerMonitor distributes encrypted-at-rest credentials while agents upload straight to object storage.</p>
              <div class="mt-3 text-xs text-zinc-400 space-y-2">
                <p>Repository namespaces use per-host prefixes and can use prefix-scoped IAM/R2 keys. No backup object passes through ServerMonitor.</p>
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
            WireGuard tunnel only — the public transport is <span class="font-mono">{tunnel?.endpoint}</span> over UDP (<span class="font-mono">BACKUP_WG_PORT={tunnel?.listen_port}</span>).
            The restic service stays inside the tunnel at <span class="font-mono">{tunnel?.server_tunnel_ip}:{tunnel?.rest_port}</span>; TCP {tunnel?.rest_port} is not a public listener.
          </div>
        {:else if !data.configured}
          <div class="rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2.5 text-xs text-zinc-400">
            This server has no gateway storage backend (<span class="font-mono">BACKUP_DIR</span> / <span class="font-mono">BACKUP_S3_BUCKET</span>). Storage-node and direct S3 repositories still work; the latter bypass ServerMonitor entirely.
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

        {@render directDestinationsSection()}

        {@render fleetSection()}

        <section>
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div>
              <h2 class="text-sm font-medium text-zinc-100">Repositories</h2>
              <p class="mt-0.5 text-xs text-zinc-500">A repository is a logical namespace. Its storage backend and effective data path are shown independently; create one per host.</p>
            </div>
          </div>

          <div class="mt-3 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
            <div class="grid grid-cols-1 sm:grid-cols-3 divide-y sm:divide-y-0 sm:divide-x divide-zinc-800">
              <div class="px-4 sm:px-5 py-3 sm:py-4 min-w-0">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Server storage backend</div>
                <div class="text-lg font-semibold text-zinc-100 mt-1">{data.storage ? (data.storage.kind === 's3' ? 'S3 gateway backend' : 'Server disk') : 'None'}</div>
                <div class="text-[11px] text-zinc-500 mt-0.5 font-mono truncate" title={data.storage?.location ?? ''}>{data.storage?.location ?? ''}</div>
              </div>
              <div class="px-4 sm:px-5 py-3 sm:py-4">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Stored</div>
                <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{bytes(data.targets.reduce((s, t) => s + t.used_bytes, 0))}</div>
                <div class="text-[11px] text-zinc-500 mt-0.5">gateway/node tracked; direct S3 is provider-measured</div>
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
                      {@const destinationNode = nodeForTarget(t)}
                      {@const destinationAvailability = nodeAvailability(destinationNode)}
                      {@const retiredNodeTargetDeletable = t.node_host_id != null && (destinationNode === null || destinationAvailability === 'removed' || destinationAvailability === 'archived')}
                      <tr class="hover:bg-zinc-900/60">
                        <td class="px-4 py-3 align-top whitespace-nowrap">
                          <div class="flex items-center gap-2">
                            <span class="h-1.5 w-1.5 rounded-full {t.revoked_at ? 'bg-rose-400' : 'bg-emerald-400'}"></span>
                            <span class="font-mono text-zinc-200">{t.name}</span>
                          </div>
                          {#if t.revoked_at}
                            <span class="ml-3.5 text-[10px] uppercase tracking-wider text-rose-300">revoked</span>
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top text-xs">
                          <div class="text-zinc-300">{t.storage_backend.label}</div>
                          <div class="mt-0.5 font-medium {t.data_path.kind === 'agent_to_s3_direct' ? 'text-emerald-300' : t.data_path.kind === 'agent_via_server_to_s3' ? 'text-amber-300' : 'text-sky-300'}">{t.data_path.label}</div>
                          {#if t.node_host_id}
                            {#if destinationNode}
                              {#if destinationAvailability}<span class="mt-1 inline-block rounded border px-1 py-px text-[10px] uppercase tracking-wider {availabilityBadge(destinationAvailability)}">{destinationAvailability}</span>{/if}
                            {:else}
                              <span class="mt-1 text-zinc-500">{t.node_hostname || `node ${t.node_host_id}`}</span>
                            {/if}
                          {/if}
                          {#if t.namespace_prefix}<div class="mt-1 max-w-xs truncate font-mono text-[10px] text-zinc-600" title={t.namespace_prefix}>{t.namespace_prefix}</div>{/if}
                          {#if t.destination_id}<div class="mt-0.5 text-[10px] text-zinc-600">{t.credential_scope === 'repository' ? 'repository-scoped credential' : 'shared destination credential'}</div>{/if}
                        </td>
                        <td class="px-4 py-3 align-top text-xs whitespace-nowrap">
                          {#if t.host_id}
                            <a href="/hosts/{t.host_id}?tab=backups" class="text-sky-300 hover:text-sky-200">{t.hostname || `host ${t.host_id}`}</a>
                          {:else}
                            <span class="text-zinc-500">—</span>
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top">
                          {#if t.destination_id}
                            <div class="text-xs text-zinc-400">provider-managed</div>
                            <div class="text-[10px] text-zinc-600 mt-0.5">traffic bypasses server</div>
                          {:else}
                          <div class="numeric text-zinc-200 text-xs">{bytes(t.used_bytes)}{#if t.quota_bytes}<span class="text-zinc-500"> / {bytes(t.quota_bytes)}</span>{/if}</div>
                          {#if t.quota_bytes}
                            <div class="mt-1.5 h-1.5 w-28 rounded-full bg-zinc-800 overflow-hidden">
                              <div class="h-full rounded-full {barTone(t.used_bytes, t.quota_bytes)}" style="width: {sharePct(t.used_bytes, t.quota_bytes)}%"></div>
                            </div>
                          {:else}
                            <div class="text-[10px] text-zinc-600 mt-0.5">no quota</div>
                          {/if}
                          {/if}
                        </td>
                        <td class="px-4 py-3 align-top text-zinc-400 text-xs numeric whitespace-nowrap">{t.destination_id ? 'at provider' : t.usage_measured_at ? timeAgo(t.usage_measured_at) : 'never'}</td>
                        <td class="px-4 py-3 align-top text-zinc-400 text-xs numeric whitespace-nowrap">{timeAgo(t.created_at)}</td>
                        <td class="px-4 py-3 align-top whitespace-nowrap">
                          <div class="flex items-center justify-end gap-1.5">
                            {#if !t.revoked_at && !t.destination_id}
                              <button
                                type="button"
                                onclick={() => openQuota(t)}
                                class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                                Quota
                              </button>
                            {/if}
                            {#if !t.node_host_id && !t.destination_id}
                              <button
                                type="button"
                                onclick={() => measure(t.id)}
                                disabled={measuring === t.id}
                                class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
                                {measuring === t.id ? 'Measuring…' : 'Measure'}
                              </button>
                            {/if}
                            {#if !t.destination_id}<button
                              type="button"
                              onclick={() => rotate(t.id)}
                              class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                              Rotate
                            </button>{/if}
                            {#if t.destination_id}
                              {#if !t.revoked_at}<button type="button" onclick={() => openRepositoryCredentials(t)} class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Replace key</button>{/if}
                              {#if t.revoked_at}
                                <button type="button" onclick={() => (toDelete = t)} title="Deletes only the control-plane record; S3 objects remain" class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Delete record</button>
                              {:else}
                                <button type="button" onclick={() => (toRevoke = t)} class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Revoke</button>
                              {/if}
                            {:else if t.used_bytes === 0 && !t.node_host_id}
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
                            {#if retiredNodeTargetDeletable}
                              <button
                                type="button"
                                onclick={() => (retiredNodeTargetToDelete = t)}
                                class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                                Delete
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
                  {@const destinationNode = nodeForTarget(t)}
                  {@const destinationAvailability = nodeAvailability(destinationNode)}
                  {@const retiredNodeTargetDeletable = t.node_host_id != null && (destinationNode === null || destinationAvailability === 'removed' || destinationAvailability === 'archived')}
                  <div class="px-4 py-3 space-y-2">
                    <div class="flex min-w-0 items-center gap-2">
                      <span class="h-1.5 w-1.5 shrink-0 rounded-full {t.revoked_at ? 'bg-rose-400' : 'bg-emerald-400'}"></span>
                      <span class="min-w-0 truncate font-mono text-sm text-zinc-200">{t.name}</span>
                      {#if t.revoked_at}
                        <span class="shrink-0 text-[10px] uppercase tracking-wider text-rose-300">revoked</span>
                      {/if}
                    </div>
                    <div class="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs">
                      <span class="text-zinc-300">{t.storage_backend.label}</span>
                      <span class="text-zinc-700">·</span>
                      <span class={t.data_path.kind === 'agent_to_s3_direct' ? 'text-emerald-300' : t.data_path.kind === 'agent_via_server_to_s3' ? 'text-amber-300' : 'text-sky-300'}>{t.data_path.label}</span>
                      {#if destinationAvailability}<span class="rounded border px-1 py-px text-[10px] uppercase tracking-wider {availabilityBadge(destinationAvailability)}">{destinationAvailability}</span>{/if}
                      <span class="text-zinc-700">·</span>
                      {#if t.host_id}
                        <a href="/hosts/{t.host_id}?tab=backups" class="text-sky-300 hover:text-sky-200">{t.hostname || `host ${t.host_id}`}</a>
                      {:else}
                        <span class="text-zinc-600">not linked</span>
                      {/if}
                    </div>
                    {#if t.namespace_prefix}<div class="truncate font-mono text-[10px] text-zinc-600">{t.namespace_prefix}</div>{/if}
                    <div>
                      {#if t.destination_id}
                        <div class="text-xs text-zinc-400">Usage measured at object-storage provider · traffic bypasses server</div>
                      {:else}
                      <div class="numeric text-xs text-zinc-200">{bytes(t.used_bytes)}{#if t.quota_bytes}<span class="text-zinc-500"> / {bytes(t.quota_bytes)}</span>{/if}</div>
                      {#if t.quota_bytes}
                        <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-zinc-800">
                          <div class="h-full rounded-full {barTone(t.used_bytes, t.quota_bytes)}" style="width: {sharePct(t.used_bytes, t.quota_bytes)}%"></div>
                        </div>
                      {/if}
                      {/if}
                    </div>
                    <div class="text-[11px] text-zinc-500 numeric">
                      measured {t.destination_id ? 'at provider' : t.usage_measured_at ? timeAgo(t.usage_measured_at) : 'never'} · created {timeAgo(t.created_at)}
                    </div>
                    <div class="flex justify-end gap-2">
                      {#if !t.revoked_at && !t.destination_id}
                        <button
                          type="button"
                          onclick={() => openQuota(t)}
                          class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                          Quota
                        </button>
                      {/if}
                      {#if !t.node_host_id && !t.destination_id}
                        <button
                          type="button"
                          onclick={() => measure(t.id)}
                          disabled={measuring === t.id}
                          class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
                          {measuring === t.id ? 'Measuring…' : 'Measure'}
                        </button>
                      {/if}
                      {#if !t.destination_id}<button
                        type="button"
                        onclick={() => rotate(t.id)}
                        class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                        Rotate
                      </button>{/if}
                      {#if t.destination_id}
                        {#if !t.revoked_at}<button type="button" onclick={() => openRepositoryCredentials(t)} class="text-[11px] px-2.5 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">Replace key</button>{/if}
                        {#if t.revoked_at}
                          <button type="button" onclick={() => (toDelete = t)} class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Delete record</button>
                        {:else}
                          <button type="button" onclick={() => (toRevoke = t)} class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">Revoke</button>
                        {/if}
                      {:else if t.used_bytes === 0 && !t.node_host_id}
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
                      {#if retiredNodeTargetDeletable}
                        <button
                          type="button"
                          onclick={() => (retiredNodeTargetToDelete = t)}
                          class="text-[11px] px-2.5 py-1.5 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                          Delete
                        </button>
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>

          <p class="mt-3 text-[11px] text-zinc-600">
            ServerMonitor and storage-node gateways enforce append-only writes. Direct S3 immutability depends on the provider IAM/Object Lock policy.
            Revocation never asks any backend to delete data; remove blobs with a separately trusted backend credential when you need to reclaim space.
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
                  <div class="text-[11px] uppercase tracking-wider text-zinc-500">Public transport</div>
                  <div class="text-lg font-semibold text-zinc-100 mt-1 font-mono truncate" title={tunnel?.endpoint}>{tunnel?.endpoint}</div>
                  <div class="text-[11px] text-zinc-500 mt-0.5">BACKUP_WG_PORT · UDP · silent to strangers</div>
                </div>
                <div class="px-4 sm:px-5 py-3 sm:py-4">
                  <div class="text-[11px] uppercase tracking-wider text-zinc-500">Internal backup service</div>
                  <div class="text-lg font-semibold text-zinc-100 mt-1 font-mono">{tunnel?.server_tunnel_ip}:{tunnel?.rest_port}</div>
                  <div class="text-[11px] text-zinc-500 mt-0.5">inside {tunnel?.subnet} · not the public dial address</div>
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
                          <td class="px-4 py-3 whitespace-nowrap">
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
                          <td class="px-4 py-3 text-right whitespace-nowrap">
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
            <p class="mt-3 text-[11px] text-zinc-600">
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
                        {@const availability = nodeAvailability(n)}
                        <tr class="hover:bg-zinc-900/60">
                          <td class="px-4 py-3 whitespace-nowrap">
                            {#if n.host_missing}
                              <span class="font-mono text-zinc-500">{n.hostname || `node ${n.host_id}`}</span>
                            {:else}
                              <a href="/hosts/{n.host_id}" class="font-mono text-zinc-200 hover:text-zinc-100">{n.hostname || `node ${n.host_id}`}</a>
                            {/if}
                            {#if n.store_dir && n.store_dir !== '/var/lib/servermonitor/backup-store'}
                              <div class="mt-1 text-[11px] font-mono text-zinc-500">{n.store_dir}</div>
                            {/if}
                          </td>
                          <td class="px-4 py-3 text-xs font-mono text-zinc-300">{n.endpoint}<span class="text-zinc-600">:{n.udp_port}/udp</span></td>
                          <td class="px-4 py-3">
                            <div class="flex items-center gap-2 text-xs">
                              <span class="h-1.5 w-1.5 rounded-full {availability ? availabilityDot(availability) : n.node_state === 'error' ? 'bg-rose-400' : n.enrolled ? 'bg-emerald-400' : 'bg-amber-400'}"></span>
                              {#if availability === 'removed'}
                                <span class="text-rose-300">host removed</span>
                              {:else if availability === 'archived'}
                                <span class="text-amber-300">host archived</span>
                              {:else if availability === 'offline'}
                                <span class="text-rose-300">offline{#if n.last_seen} — last seen <span class="numeric">{timeAgo(n.last_seen)}</span>{/if}</span>
                              {:else if n.node_state === 'error'}
                                <span class="text-rose-300">endpoint failed</span>
                              {:else if n.enrolled}
                                <span class="text-emerald-300 font-mono">{n.tunnel_ip}</span>
                              {:else}
                                <span class="text-amber-300">coming online…</span>
                              {/if}
                            </div>
                            {#if !availability && n.node_state === 'error' && n.node_error}
                              <div class="mt-1 max-w-[280px] truncate font-mono text-[11px] text-rose-300/80" title={n.node_error}>{n.node_error}</div>
                            {/if}
                          </td>
                          <td class="px-4 py-3 text-xs text-zinc-300 numeric">{n.target_count}</td>
                          <td class="px-4 py-3 text-xs text-zinc-300 numeric">{bytes(n.used_bytes)}</td>
                          <td class="px-4 py-3 text-right whitespace-nowrap">
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
                    {@const availability = nodeAvailability(n)}
                    <div class="px-4 py-3 space-y-2">
                      <div class="flex items-center justify-between gap-3">
                        {#if n.host_missing}
                          <span class="min-w-0 truncate font-mono text-sm text-zinc-500">{n.hostname || `node ${n.host_id}`}</span>
                        {:else}
                          <a href="/hosts/{n.host_id}" class="min-w-0 truncate font-mono text-sm text-zinc-200 hover:text-zinc-100">{n.hostname || `node ${n.host_id}`}</a>
                        {/if}
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
                        <span class="h-1.5 w-1.5 rounded-full {availability ? availabilityDot(availability) : n.node_state === 'error' ? 'bg-rose-400' : n.enrolled ? 'bg-emerald-400' : 'bg-amber-400'}"></span>
                        {#if availability === 'removed'}
                          <span class="text-rose-300">host removed</span>
                        {:else if availability === 'archived'}
                          <span class="text-amber-300">host archived</span>
                        {:else if availability === 'offline'}
                          <span class="text-rose-300">offline{#if n.last_seen} — last seen <span class="numeric">{timeAgo(n.last_seen)}</span>{/if}</span>
                        {:else if n.node_state === 'error'}
                          <span class="text-rose-300">endpoint failed</span>
                        {:else if n.enrolled}
                          <span class="font-mono text-emerald-300">{n.tunnel_ip}</span>
                        {:else}
                          <span class="text-amber-300">coming online…</span>
                        {/if}
                      </div>
                      {#if !availability && n.node_state === 'error' && n.node_error}
                        <div class="truncate font-mono text-[11px] text-rose-300/80" title={n.node_error}>{n.node_error}</div>
                      {/if}
                      <div class="text-[11px] text-zinc-500 numeric">{n.target_count} {n.target_count === 1 ? 'repository' : 'repositories'} · {bytes(n.used_bytes)} stored</div>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
            <p class="mt-3 text-[11px] text-zinc-600">
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
      <button type="button" onclick={() => rotated && copy(rotated.password ?? '', 'rot')} class="text-[11px] px-2 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200">{copied === 'rot' ? 'copied' : 'Copy password'}</button>
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
  {#if toRevoke?.destination_id}
    <p class="text-sm text-zinc-300">Stop centrally delivering <span class="font-mono text-zinc-100">{toRevoke.name}</span> to its host?</p>
    <p class="mt-2 text-xs text-zinc-500">The agent removes its local managed credential on the next poll. ServerMonitor never contacts S3 and does not delete any object under <span class="font-mono">{toRevoke.namespace_prefix}</span>.</p>
  {:else}
    <p class="text-sm text-zinc-300">
      Revoke the credential for <span class="font-mono text-zinc-100">{toRevoke?.name}</span>? Its host can no longer upload.
      ServerMonitor does not delete stored backups; they can still be restored from another credentialed machine.
    </p>
  {/if}
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
  {#if toDelete?.destination_id}
    <p class="text-sm text-zinc-300">Delete the revoked control-plane namespace <span class="font-mono text-zinc-100">{toDelete.name}</span>?</p>
    <p class="mt-2 text-xs text-zinc-500">This removes only ServerMonitor's encrypted credential and assignment record. S3/R2 objects remain under <span class="font-mono">{toDelete.namespace_prefix}</span> and must be retained, restored, or deleted at the provider.</p>
  {:else}
  <p class="text-sm text-zinc-300">
    Permanently delete <span class="font-mono text-zinc-100">{toDelete?.name}</span>? It holds no backups, so nothing is
    lost — this just removes the credential and its entry.
  </p>
  <p class="mt-2 text-xs text-zinc-500">
    Only empty repositories can be deleted. If an upload has landed since this list loaded, the delete is refused and you can
    revoke instead.
  </p>
  {/if}
{/snippet}

<ConfirmDialog
  open={destinationToDelete !== null}
  title="Delete direct S3 destination"
  body={destinationDeleteBody}
  confirmLabel="Delete destination"
  danger
  onconfirm={deleteDestination}
  onclose={() => (destinationToDelete = null)} />

{#snippet destinationDeleteBody()}
  <p class="text-sm text-zinc-300">
    Delete <span class="font-mono text-zinc-100">{destinationToDelete?.name}</span> and its encrypted control-plane credential?
  </p>
  <p class="mt-2 text-xs text-zinc-500">This is allowed only when no repository namespaces reference it. It never contacts S3 and never deletes bucket objects.</p>
{/snippet}

<ConfirmDialog
  open={retiredNodeTargetToDelete !== null}
  title="Delete repository record"
  body={retiredNodeDeleteBody}
  confirmLabel="Delete"
  danger
  onconfirm={doDeleteRetiredNodeTarget}
  onclose={() => (retiredNodeTargetToDelete = null)} />

{#snippet retiredNodeDeleteBody()}
  <p class="text-sm text-zinc-300">
    This deletes only the server-side record and credential for{' '}<span class="font-mono text-zinc-100">{retiredNodeTargetToDelete?.name}</span>.{' '}
    Snapshots on the node's disk are not touched, and this server holds no copy.
  </p>
  <p class="mt-2 text-xs text-amber-300">
    If the disk is recoverable, keep this repository until the store has been salvaged.
  </p>
{/snippet}

<ConfirmDialog
  open={repositoryCredentialsFor !== null}
  title="Replace repository-scoped S3 credential"
  body={repositoryCredentialsBody}
  confirmLabel="Replace credential"
  onconfirm={replaceRepositoryCredentials}
  onclose={() => (repositoryCredentialsFor = null)} />

{#snippet repositoryCredentialsBody()}
  <p class="text-xs text-zinc-500">The old key for <span class="font-mono text-zinc-300">{repositoryCredentialsFor?.name}</span> is replaced atomically in the encrypted control plane. It is never returned by the admin API.</p>
  <div class="mt-3 space-y-3">
    <div>
      <label for="repo-key" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Access key ID</label>
      <input id="repo-key" type="password" bind:value={repositoryAccessKeyId} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
    </div>
    <div>
      <label for="repo-secret" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Secret access key</label>
      <input id="repo-secret" type="password" bind:value={repositorySecretAccessKey} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
    </div>
    <div>
      <label for="repo-session" class="block text-[11px] uppercase tracking-wider text-zinc-500 mb-1">Session token <span class="normal-case text-zinc-600">(optional)</span></label>
      <input id="repo-session" type="password" bind:value={repositorySessionToken} autocomplete="new-password" class="w-full rounded-md bg-zinc-950 border border-zinc-800 px-3 py-2 text-sm font-mono" />
    </div>
  </div>
  {#if repositoryCredentialError}<div class="mt-3 text-xs text-rose-300">{repositoryCredentialError}</div>{/if}
{/snippet}

<ConfirmDialog
  open={toEditQuota !== null}
  title="Edit repository quota"
  body={quotaBody}
  confirmLabel="Save quota"
  onconfirm={saveQuota}
  onclose={() => (toEditQuota = null)} />

{#snippet quotaBody()}
  <p class="text-sm text-zinc-300">
    Upload quota for <span class="font-mono text-zinc-100">{toEditQuota?.name}</span>.
  </p>
  <div class="mt-3">
    <label for="eq-quota" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Quota <span class="text-zinc-600 normal-case">(GiB)</span></label>
    <input
      id="eq-quota"
      type="number"
      min="0"
      step="1"
      bind:value={editQuotaGiB}
      placeholder="unlimited"
      class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
    <p class="mt-1.5 text-[11px] text-zinc-600">Uploads are refused once the repo exceeds this. Leave blank for no limit.</p>
  </div>
  {#if toEditQuota && editQuotaGiB != null && editQuotaGiB > 0 && Math.round(editQuotaGiB * 1024 ** 3) < toEditQuota.used_bytes}
    <div class="mt-3 rounded-md border border-amber-900/50 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
      New limit is below current usage ({bytes(toEditQuota.used_bytes)}). Uploads stay blocked until usage drops, but existing backups are kept.
    </div>
  {/if}
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
