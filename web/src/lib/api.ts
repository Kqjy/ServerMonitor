export interface CollectorStatus {
  state: string;
  message?: string;
}

export interface Host {
  id: number;
  hostname: string;
  os?: string;
  arch?: string;
  kernel?: string;
  agent_version?: string;
  latest_agent_version?: string;
  update_available?: boolean;
  auto_upgrade: boolean;
  supports_remote_upgrade: boolean;
  externally_managed?: boolean;
  upgrade_stalled?: boolean;
  upgrade_pending?: boolean;
  upgrading?: boolean;
  sample_interval_s: number;
  enabled_collectors?: string[];
  collector_status?: Record<string, CollectorStatus>;
  tags?: Record<string, string>;
  last_seen?: string;
  created_at: string;
  firing_alerts?: number;
  firing_severity?: 'info' | 'warning' | 'critical' | '';
}

export interface ActiveAlert {
  rule_id: number;
  rule_name: string;
  severity: 'info' | 'warning' | 'critical' | string;
  metric: string;
  label_key?: string;
  since: string;
  value?: number;
}

export interface SeriesPoint {
  ts: string;
  v: number;
}

export interface SeriesResp {
  host_id: number;
  metric: string;
  unit: string;
  step_sec: number;
  points: SeriesPoint[];
}

export interface SeriesEntry {
  labels: Record<string, string>;
  points: SeriesPoint[];
}

export interface MultiSeriesResp {
  host_id: number;
  metric: string;
  unit: string;
  step_sec: number;
  split_by?: string;
  series: SeriesEntry[];
}

export interface BatchSeriesResp {
  metric: string;
  unit: string;
  step_sec: number;
  hosts: Record<string, { points: SeriesPoint[] }>;
}

export interface ProcessRow {
  pid: number;
  name: string;
  user?: string;
  cmdline?: string;
  cpu_pct: number;
  mem_rss: number;
  status?: string;
  nthreads?: number;
  time: string;
}

export interface ContainerRow {
  cid: string;
  name: string;
  image?: string;
  state: string;
  cpu_pct: number;
  mem_used: number;
  mem_limit?: number;
  rx_bytes?: number;
  tx_bytes?: number;
  time: string;
}

export interface PortRow {
  proto: string;
  addr: string;
  port: number;
  pid?: number;
  process?: string;
  time: string;
}

export interface BackupSnapshot {
  id: string;
  time?: string;
  paths?: string[];
  size_bytes?: number;
  added_bytes?: number;
  file_count?: number;
  duration_s?: number;
}

export interface BackupRepoStatus {
  name: string;
  engine?: string;
  last_started?: string;
  last_finished?: string;
  last_success?: string;
  success: boolean;
  error?: string;
  duration_s?: number;
  added_bytes?: number;
  total_bytes?: number;
  snapshot_count?: number;
  check_last?: string;
  check_success?: boolean;
  tunnel?: boolean;
  next_run?: string;
  snapshots?: BackupSnapshot[];
}

export interface BackupRepoRow {
  repo: string;
  updated_at: string;
  status: BackupRepoStatus;
}

export interface BackupsResp {
  repos: BackupRepoRow[];
}

export interface BackupTargetStorage {
  kind: string;
  location: string;
}

export interface BackupTargetTLS {
  mode: string;
  domain?: string;
  secure: boolean;
}

export interface BackupTarget {
  id: number;
  name: string;
  host_id?: number;
  hostname?: string;
  node_host_id?: number;
  node_hostname?: string;
  quota_bytes?: number;
  used_bytes: number;
  usage_measured_at?: string;
  created_at: string;
  revoked_at?: string;
}

export interface BackupNode {
  host_id: number;
  hostname: string;
  udp_port: number;
  endpoint: string;
  store_dir?: string;
  tunnel_ip?: string;
  enrolled: boolean;
  target_count: number;
  used_bytes: number;
  created_at: string;
}

export interface BackupTargetsResp {
  configured: boolean;
  storage?: BackupTargetStorage;
  tls: BackupTargetTLS;
  targets: BackupTarget[];
}

export interface BackupCredential {
  id: number;
  name: string;
  password: string;
}

export interface BackupTunnelPeer {
  host_id: number;
  hostname: string;
  tunnel_ip: string;
  enrolled_at: string;
  last_handshake?: string;
  rx_bytes: number;
  tx_bytes: number;
  connected: boolean;
  via?: string;
}

export interface BackupTunnelResp {
  enabled: boolean;
  public_http: boolean;
  listen_port?: number;
  endpoint?: string;
  subnet?: string;
  server_public_key?: string;
  server_tunnel_ip?: string;
  rest_port?: number;
  peers: BackupTunnelPeer[];
}

export interface ProcessSeriesPoint {
  ts: string;
  cpu_pct: number;
  mem_rss: number;
}

export interface ProcessSeriesResp {
  host_id: number;
  pid: number;
  name?: string;
  cmdline?: string;
  step_sec: number;
  points: ProcessSeriesPoint[];
}

export interface ContainerSeriesPoint {
  ts: string;
  cpu_pct: number;
  mem_used: number;
  rx_rate: number;
  tx_rate: number;
}

export interface ContainerSeriesResp {
  host_id: number;
  cid: string;
  name?: string;
  image?: string;
  step_sec: number;
  points: ContainerSeriesPoint[];
}

export interface AlertRule {
  id: number;
  name: string;
  host_selector: unknown;
  metric: string;
  label_selector: Record<string, string>;
  comparator: string;
  threshold: number;
  window_s: number;
  for_s: number;
  agg: string;
  severity: string;
  cooldown_s: number;
  channel_ids: number[];
  enabled: boolean;
}

export type AlertRuleInput = Omit<AlertRule, 'id'>;

export interface AlertHistoryRow {
  id: number;
  rule_id: number;
  rule_name: string;
  host_id: number;
  hostname: string;
  label_key?: string;
  fired_at: string;
  resolved_at?: string;
  value: number;
  labels: Record<string, string>;
  severity: string;
}

export interface Channel {
  id: number;
  name: string;
  kind: 'smtp' | 'webhook';
  config: unknown;
  enabled: boolean;
}

export type ChannelInput = Omit<Channel, 'id'>;

export interface MetricMeta {
  id: number;
  name: string;
  unit: string;
}

export interface IngestStats {
  queued: number;
  flushed: number;
  dropped: number;
}

export interface AuthStatus {
  initialized: boolean;
  version?: string;
}

export interface AgentPlatform {
  id: string;
  os: string;
  arch: string;
  label: string;
  size: number;
}

export interface ServerInfo {
  url: string;
  version: string;
}

export interface Me {
  username: string;
  role: string;
}

export interface RetentionPolicy {
  key: string;
  label: string;
  target: string;
  env: string;
  configured: string;
  default: string;
  description: string;
}

export interface RetentionResp {
  policies: RetentionPolicy[];
  interval_format: string;
  requires_restart: boolean;
}

export interface StorageTable {
  schema: string;
  name: string;
  label: string;
  kind: string;
  total_bytes: number;
  table_bytes: number;
  index_bytes: number;
  approx_rows: number;
  chunks: number;
  compressed_chunks: number;
  uncompressed_bytes: number;
  compression_ratio: number;
}

export interface StorageArchive {
  configured: boolean;
  objects: number;
  total_bytes: number;
  row_count: number;
  oldest: string | null;
  newest: string | null;
}

export interface StorageResp {
  captured_at: string;
  database_bytes: number;
  tables_total_bytes: number;
  other_database_bytes: number;
  tables: StorageTable[];
  archive: StorageArchive;
  warnings?: string[];
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const base = '';

function readCookie(name: string): string {
  if (typeof document === 'undefined') return '';
  const prefix = name + '=';
  for (const part of document.cookie.split(';')) {
    const trimmed = part.trim();
    if (trimmed.startsWith(prefix)) return decodeURIComponent(trimmed.slice(prefix.length));
  }
  return '';
}

function isMutation(method: string | undefined): boolean {
  const m = (method ?? 'GET').toUpperCase();
  return m !== 'GET' && m !== 'HEAD' && m !== 'OPTIONS';
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json', ...(init?.headers as Record<string, string> ?? {}) };
  if (isMutation(init?.method)) {
    const token = readCookie('sm_csrf');
    if (token) headers['X-CSRF-Token'] = token;
  }
  const res = await fetch(base + path, {
    credentials: 'same-origin',
    ...init,
    headers
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    let msg = text;
    try {
      msg = JSON.parse(text)?.error ?? text;
    } catch {}
    throw new ApiError(res.status, msg || `${res.status} ${res.statusText}`);
  }
  if (res.status === 204) return undefined as unknown as T;
  return (await res.json()) as T;
}

export const api = {
  authStatus: () => request<AuthStatus>('/api/v1/auth/status'),
  me: () => request<Me>('/api/v1/auth/me'),
  setup: (username: string, password: string) =>
    request<Me>('/api/v1/auth/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    }),
  login: (username: string, password: string) =>
    request<Me>('/api/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    }),
  logout: () => request<void>('/api/v1/auth/logout', { method: 'POST' }),
  changePassword: (current_password: string, new_password: string) =>
    request<void>('/api/v1/auth/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password, new_password })
    }),
  hosts: () => request<Host[]>('/api/v1/hosts'),
  host: (id: number) => request<Host>(`/api/v1/hosts/${id}`),
  hostActiveAlerts: (id: number) => request<ActiveAlert[]>(`/api/v1/hosts/${id}/alerts/active`),
  series: (params: {
    host: number;
    metric: string;
    from?: string;
    to?: string;
    step?: number;
    labels?: Record<string, string>;
    signal?: AbortSignal;
  }) => {
    const q = new URLSearchParams();
    q.set('host', String(params.host));
    q.set('metric', params.metric);
    if (params.from) q.set('from', params.from);
    if (params.to) q.set('to', params.to);
    if (params.step) q.set('step', String(params.step));
    if (params.labels) q.set('labels', JSON.stringify(params.labels));
    return request<SeriesResp>(`/api/v1/series?${q}`, { signal: params.signal });
  },
  seriesBatch: async (params: {
    hosts: number[];
    metric: string;
    from?: string;
    to?: string;
    step?: number;
    agg?: 'avg' | 'max';
    signal?: AbortSignal;
  }): Promise<BatchSeriesResp> => {
    const CHUNK = 200;
    const fetchChunk = (ids: number[]) => {
      const q = new URLSearchParams();
      q.set('hosts', ids.join(','));
      q.set('metric', params.metric);
      if (params.from) q.set('from', params.from);
      if (params.to) q.set('to', params.to);
      if (params.step) q.set('step', String(params.step));
      if (params.agg) q.set('agg', params.agg);
      return request<BatchSeriesResp>(`/api/v1/series/batch?${q}`, { signal: params.signal });
    };
    if (params.hosts.length <= CHUNK) return fetchChunk(params.hosts);
    const chunks: number[][] = [];
    for (let i = 0; i < params.hosts.length; i += CHUNK) {
      chunks.push(params.hosts.slice(i, i + CHUNK));
    }
    const settled = await Promise.allSettled(chunks.map(fetchChunk));
    const merged: BatchSeriesResp = { metric: params.metric, unit: '', step_sec: 0, hosts: {} };
    for (const s of settled) {
      if (s.status !== 'fulfilled') continue;
      merged.unit = s.value.unit;
      merged.step_sec = Math.max(merged.step_sec, s.value.step_sec);
      Object.assign(merged.hosts, s.value.hosts);
    }
    return merged;
  },
  seriesMulti: (params: {
    host: number;
    metric: string;
    from?: string;
    to?: string;
    step?: number;
    splitBy?: string;
    signal?: AbortSignal;
  }) => {
    const q = new URLSearchParams();
    q.set('host', String(params.host));
    q.set('metric', params.metric);
    if (params.from) q.set('from', params.from);
    if (params.to) q.set('to', params.to);
    if (params.step) q.set('step', String(params.step));
    if (params.splitBy) q.set('split_by', params.splitBy);
    return request<MultiSeriesResp>(`/api/v1/series/multi?${q}`, { signal: params.signal });
  },
  processes: (
    hostId: number,
    opts: { limit?: number; at?: string; dir?: 'prev' | 'next'; signal?: AbortSignal } = {}
  ) => {
    const q = new URLSearchParams();
    q.set('limit', String(opts.limit ?? 50));
    if (opts.at) q.set('at', opts.at);
    if (opts.dir) q.set('dir', opts.dir);
    return request<ProcessRow[]>(`/api/v1/hosts/${hostId}/processes?${q}`, { signal: opts.signal });
  },
  containers: (hostId: number, opts: { at?: string; dir?: 'prev' | 'next'; signal?: AbortSignal } = {}) => {
    const q = new URLSearchParams();
    if (opts.at) q.set('at', opts.at);
    if (opts.dir) q.set('dir', opts.dir);
    const qs = q.toString();
    return request<ContainerRow[]>(`/api/v1/hosts/${hostId}/containers${qs ? `?${qs}` : ''}`, { signal: opts.signal });
  },
  ports: (hostId: number, opts: { at?: string; dir?: 'prev' | 'next'; signal?: AbortSignal } = {}) => {
    const q = new URLSearchParams();
    if (opts.at) q.set('at', opts.at);
    if (opts.dir) q.set('dir', opts.dir);
    const qs = q.toString();
    return request<PortRow[]>(`/api/v1/hosts/${hostId}/ports${qs ? `?${qs}` : ''}`, { signal: opts.signal });
  },
  backups: (hostId: number, opts: { signal?: AbortSignal } = {}) =>
    request<BackupsResp>(`/api/v1/hosts/${hostId}/backups`, { signal: opts.signal }),
  processSeries: (
    hostId: number,
    pid: number,
    opts: { from?: string; to?: string; step?: number; name?: string; anchor?: string; signal?: AbortSignal } = {}
  ) => {
    const q = new URLSearchParams();
    if (opts.from) q.set('from', opts.from);
    if (opts.to) q.set('to', opts.to);
    if (opts.step) q.set('step', String(opts.step));
    if (opts.name) q.set('name', opts.name);
    if (opts.anchor) q.set('anchor', opts.anchor);
    return request<ProcessSeriesResp>(
      `/api/v1/hosts/${hostId}/processes/${pid}/series?${q}`,
      { signal: opts.signal }
    );
  },
  containerSeries: (
    hostId: number,
    cid: string,
    opts: { from?: string; to?: string; step?: number; signal?: AbortSignal } = {}
  ) => {
    const q = new URLSearchParams();
    if (opts.from) q.set('from', opts.from);
    if (opts.to) q.set('to', opts.to);
    if (opts.step) q.set('step', String(opts.step));
    return request<ContainerSeriesResp>(
      `/api/v1/hosts/${hostId}/containers/${encodeURIComponent(cid)}/series?${q}`,
      { signal: opts.signal }
    );
  },

  alertRules: () => request<AlertRule[]>('/api/v1/alerts'),
  alertCreate: (r: AlertRuleInput) =>
    request<{ id: number }>('/api/v1/alerts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(r)
    }),
  alertUpdate: (id: number, r: AlertRuleInput) =>
    request<void>(`/api/v1/alerts/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(r)
    }),
  alertDelete: (id: number) => request<void>(`/api/v1/alerts/${id}`, { method: 'DELETE' }),
  alertHistory: (limit = 100) => request<AlertHistoryRow[]>(`/api/v1/alerts/history?limit=${limit}`),

  channels: () => request<Channel[]>('/api/v1/channels'),
  channelCreate: (c: ChannelInput) =>
    request<{ id: number }>('/api/v1/channels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c)
    }),
  channelUpdate: (id: number, c: ChannelInput) =>
    request<void>(`/api/v1/channels/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c)
    }),
  channelDelete: (id: number) => request<void>(`/api/v1/channels/${id}`, { method: 'DELETE' }),
  metrics: () => request<MetricMeta[]>('/api/v1/metrics'),
  stats: () => request<IngestStats>('/api/v1/stats'),
  registerHost: (hostname: string, sample_interval_s = 10) =>
    request<{ host_id: number; token: string; sample_interval_s: number }>('/api/v1/admin/hosts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hostname, sample_interval_s })
    }),
  updateHost: (id: number, patch: { hostname?: string; sample_interval_s?: number; auto_upgrade?: boolean }) =>
    request<Host>(`/api/v1/admin/hosts/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch)
    }),
  requestHostUpgrade: (id: number) =>
    request<Host>(`/api/v1/admin/hosts/${id}/upgrade`, { method: 'POST' }),
  deleteHost: (id: number) =>
    request<void>(`/api/v1/admin/hosts/${id}`, { method: 'DELETE' }),
  agentPlatforms: () => request<AgentPlatform[]>('/api/v1/agent/platforms'),
  serverInfo: () => request<ServerInfo>('/api/v1/server/info'),
  retention: () => request<RetentionResp>('/api/v1/retention'),
  storage: () => request<StorageResp>('/api/v1/storage'),
  backupTargets: () => request<BackupTargetsResp>('/api/v1/backup-targets'),
  backupTargetCreate: (body: { name: string; host_id?: number; quota_bytes?: number; node_host_id?: number }) =>
    request<BackupCredential>('/api/v1/backup-targets', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }),
  backupTargetRotate: (id: number) =>
    request<BackupCredential>(`/api/v1/backup-targets/${id}/rotate`, { method: 'POST' }),
  backupTargetMeasure: (id: number) =>
    request<BackupTarget>(`/api/v1/backup-targets/${id}/measure`, { method: 'POST' }),
  backupTargetRevoke: (id: number) => request<void>(`/api/v1/backup-targets/${id}/revoke`, { method: 'POST' }),
  backupTargetDelete: (id: number) => request<void>(`/api/v1/backup-targets/${id}`, { method: 'DELETE' }),
  backupTunnel: () => request<BackupTunnelResp>('/api/v1/backup-tunnel'),
  backupTunnelPeerRevoke: (hostId: number) =>
    request<void>(`/api/v1/backup-tunnel/peers/${hostId}`, { method: 'DELETE' }),
  backupNodes: () => request<{ nodes: BackupNode[] }>('/api/v1/backup-nodes'),
  backupNodePromote: (body: {
    host_id: number;
    endpoint: string;
    udp_port?: number;
    store_dir?: string;
  }) =>
    request<void>('/api/v1/backup-nodes', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }),
  backupNodeUpdate: (hostId: number, body: { endpoint: string; udp_port?: number }) =>
    request<void>(`/api/v1/backup-nodes/${hostId}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }),
  backupNodeDemote: (hostId: number) =>
    request<void>(`/api/v1/backup-nodes/${hostId}`, { method: 'DELETE' })
};
