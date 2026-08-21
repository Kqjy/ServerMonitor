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
  cpu_model?: string;
  cpu_cores?: number;
  cpu_threads?: number;
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
  archived_at?: string;
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

export interface SeriesGroupMetricResp {
  metric: string;
  unit: string;
  step_sec: number;
  split_by?: string;
  series: SeriesEntry[];
}

export interface SeriesGroupResp {
  host_id: number;
  step_sec: number;
  metrics: Record<string, SeriesGroupMetricResp>;
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
  paths?: string[];
  excludes?: string[];
  one_file_system?: boolean;
  path_stats?: { path: string; bytes: number; files: number }[];
  stats_snapshot?: string;
  stats_at?: string;
  snapshots?: BackupSnapshot[];
}

export interface BackupBrowseEntry {
  name: string;
  type: string;
  size?: number;
  mtime?: string;
}

export interface BackupBrowseResult {
  entries?: BackupBrowseEntry[];
  truncated?: boolean;
  error?: string;
  error_kind?: string;
}

export interface BackupBrowseJobStatus {
  status: string;
  result?: BackupBrowseResult;
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
  destination_id?: number;
  destination_name?: string;
  destination_kind?: string;
  namespace_prefix?: string;
  managed: boolean;
  credential_scope?: 'destination' | 'repository';
  storage_backend: {
    kind: string;
    label: string;
    location?: string;
  };
  repository_namespace: {
    name: string;
    prefix?: string;
  };
  data_path: {
    kind: string;
    label: string;
    hops: string[];
    transport?: string;
  };
  quota_bytes?: number;
  used_bytes: number;
  usage_measured_at?: string;
  created_at: string;
  revoked_at?: string;
}

export interface BackupDestination {
  id: number;
  name: string;
  kind: 'direct_s3';
  endpoint?: string;
  bucket: string;
  region?: string;
  prefix_template: string;
  use_path_style: boolean;
  credentials_configured: boolean;
  repository_count: number;
  data_path: string;
  created_at: string;
  updated_at: string;
}

export interface BackupNode {
  host_id: number;
  hostname: string;
  udp_port: number;
  endpoint: string;
  store_dir?: string;
  tunnel_ip?: string;
  enrolled: boolean;
  last_seen?: string;
  sample_interval_s?: number;
  archived?: boolean;
  host_missing?: boolean;
  node_state?: string;
  node_error?: string;
  reported_at?: string;
  target_count: number;
  used_bytes: number;
  created_at: string;
}

export interface BackupTargetsResp {
  configured: boolean;
  storage?: BackupTargetStorage;
  tls: BackupTargetTLS;
  targets: BackupTarget[];
  repositories: BackupTarget[];
  destinations: BackupDestination[];
}

export interface BackupCredential {
  id: number;
  name: string;
  password?: string;
  managed?: boolean;
  data_path?: string;
}

export interface BackupS3CredentialsInput {
  access_key_id: string;
  secret_access_key: string;
  session_token?: string;
}

export interface BackupDestinationInput {
  name: string;
  kind: 'direct_s3';
  endpoint?: string;
  bucket: string;
  region?: string;
  prefix_template?: string;
  use_path_style?: boolean;
  credentials?: BackupS3CredentialsInput;
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
  cpu_max: number;
  mem_used: number;
  mem_max: number;
  rx_rate: number;
  tx_rate: number;
  io_rate_max: number;
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
  owner: string;
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
  last_attempt_at?: string;
  last_success_at?: string;
  last_run_ok?: boolean;
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

export interface IPBanSourceStatus {
  name: string;
  state: string;
  message?: string;
}

export interface IPBanSettings {
  enabled: boolean;
  enforce: boolean;
  contribute: boolean;
  apply_fleet: boolean;
  mode: string;
  max_retry: number;
  find_time_s: number;
  ban_time_s: number;
  ban_time_max_s: number;
  ban_private: boolean;
  fleet_min_hosts: number;
  fleet_min_bans: number;
  fleet_ttl_s: number;
  allowlist: string[];
  version: number;
  updated_at: string;
  client_ip: string;
}

export type IPBanSettingsInput = Partial<Omit<IPBanSettings, 'version' | 'updated_at' | 'client_ip'>>;

export interface IPBanHostPolicy {
  detect: boolean | null;
  enforce: boolean | null;
  contribute: boolean | null;
  apply_fleet: boolean | null;
}

export interface IPBanEffectivePolicy {
  detect: boolean;
  enforce: boolean;
  contribute: boolean;
  apply_fleet: boolean;
}

export interface IPBanHost {
  host_id: number;
  hostname: string;
  os: string;
  agent_version: string;
  last_seen: string | null;
  sample_interval_s: number;
  archived: boolean;
  override: IPBanHostPolicy;
  effective: IPBanEffectivePolicy;
  supported: boolean;
  detect_state: string;
  detect_message?: string;
  enforce_state: string;
  enforce_message?: string;
  sources: IPBanSourceStatus[];
  active_local: number;
  fleet_applied: number;
  applied_version: number;
  reported_at: string | null;
  config_stale: boolean;
}

export interface IPBanActive {
  host_id: number;
  hostname: string;
  ip: string;
  source: string;
  failures: number;
  user?: string;
  banned_at: string;
  expires_at: string;
  enforced: boolean;
  repeat_count: number;
  fleet: boolean;
}

export interface IPBanFleet {
  ip: string;
  first_seen: string;
  last_seen: string;
  host_count: number;
  ban_count: number;
  expires_at: string;
  source: string;
  note?: string;
  created_by?: string;
  active_hosts: number;
}

export interface IPBanEvent {
  id: number;
  time: string;
  host_id: number | null;
  hostname?: string;
  ip: string;
  action: string;
  source?: string;
  failures?: number;
  user?: string;
  expires_at: string | null;
  enforced: boolean;
  repeat_count?: number;
  actor?: string;
  note?: string;
}

export interface IPBanSummary {
  hosts_total: number;
  hosts_detecting: number;
  hosts_enforcing: number;
  hosts_observing: number;
  hosts_blocked: number;
  hosts_not_capable: number;
  active_local: number;
  fleet_size: number;
  bans_24h: number;
  fleet_bans_24h: number;
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
  hosts: (scope?: 'include' | 'only') =>
    request<Host[]>(`/api/v1/hosts${scope ? `?archived=${scope}` : ''}`),
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
  seriesGroup: (params: {
    host: number;
    series: { metric: string; splitBy?: string }[];
    from?: string;
    to?: string;
    step?: number;
    signal?: AbortSignal;
  }) => {
    const q = new URLSearchParams();
    q.set('host', String(params.host));
    for (const item of params.series) {
      q.append('series', item.splitBy ? `${item.metric}:${item.splitBy}` : item.metric);
    }
    if (params.from) q.set('from', params.from);
    if (params.to) q.set('to', params.to);
    if (params.step) q.set('step', String(params.step));
    return request<SeriesGroupResp>(`/api/v1/series/group?${q}`, { signal: params.signal });
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
  backupBrowseStart: (
    hostId: number,
    body: { repo: string; snapshot: string; path: string },
    opts: { signal?: AbortSignal } = {}
  ) =>
    request<{ job_id: string }>(`/api/v1/hosts/${hostId}/backups/browse`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: opts.signal
    }),
  backupBrowseJob: (hostId: number, jobId: string, opts: { signal?: AbortSignal } = {}) =>
    request<BackupBrowseJobStatus>(`/api/v1/hosts/${hostId}/backups/browse/${jobId}`, { signal: opts.signal }),
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
  alertHistoryClear: (olderThanDays?: number) => request<{ deleted: number }>(`/api/v1/alerts/history${olderThanDays ? `?older_than_days=${olderThanDays}` : ''}`, { method: 'DELETE' }),

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
  archiveHost: (id: number) =>
    request<Host>(`/api/v1/admin/hosts/${id}/archive`, { method: 'POST' }),
  unarchiveHost: (id: number) =>
    request<Host>(`/api/v1/admin/hosts/${id}/unarchive`, { method: 'POST' }),
  agentPlatforms: () => request<AgentPlatform[]>('/api/v1/agent/platforms'),
  serverInfo: () => request<ServerInfo>('/api/v1/server/info'),
  retention: () => request<RetentionResp>('/api/v1/retention'),
  storage: () => request<StorageResp>('/api/v1/storage'),
  backupTargets: () => request<BackupTargetsResp>('/api/v1/backup-repositories'),
  backupTargetCreate: (body: { name: string; host_id?: number; quota_bytes?: number; node_host_id?: number; destination_id?: number; s3_credentials?: BackupS3CredentialsInput }) =>
    request<BackupCredential>('/api/v1/backup-repositories', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }),
  backupTargetRotate: (id: number) =>
    request<BackupCredential>(`/api/v1/backup-repositories/${id}/rotate`, { method: 'POST' }),
  backupTargetMeasure: (id: number) =>
    request<BackupTarget>(`/api/v1/backup-repositories/${id}/measure`, { method: 'POST' }),
  backupTargetSetQuota: (id: number, quota_bytes: number | null) =>
    request<BackupTarget>(`/api/v1/backup-repositories/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ quota_bytes })
    }),
  backupTargetRevoke: (id: number) => request<void>(`/api/v1/backup-repositories/${id}/revoke`, { method: 'POST' }),
  backupTargetDelete: (id: number) => request<void>(`/api/v1/backup-repositories/${id}`, { method: 'DELETE' }),
  backupTargetSetCredentials: (id: number, credentials: BackupS3CredentialsInput) =>
    request<void>(`/api/v1/backup-repositories/${id}/credentials`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(credentials)
    }),
  backupDestinations: () => request<{ destinations: BackupDestination[] }>('/api/v1/backup-destinations'),
  backupDestinationCreate: (body: BackupDestinationInput) =>
    request<BackupDestination>('/api/v1/backup-destinations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }),
  backupDestinationSetCredentials: (id: number, credentials: BackupS3CredentialsInput) =>
    request<BackupDestination>(`/api/v1/backup-destinations/${id}/credentials`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(credentials)
    }),
  backupDestinationDelete: (id: number) => request<void>(`/api/v1/backup-destinations/${id}`, { method: 'DELETE' }),
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
    request<void>(`/api/v1/backup-nodes/${hostId}`, { method: 'DELETE' }),

  ipbanSettings: (opts: { signal?: AbortSignal } = {}) =>
    request<IPBanSettings>('/api/v1/ipban/settings', { signal: opts.signal }),
  ipbanUpdateSettings: (input: IPBanSettingsInput) =>
    request<IPBanSettings>('/api/v1/ipban/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input)
    }),
  ipbanSummary: (opts: { signal?: AbortSignal } = {}) =>
    request<IPBanSummary>('/api/v1/ipban/summary', { signal: opts.signal }),
  ipbanHosts: (opts: { signal?: AbortSignal } = {}) =>
    request<IPBanHost[]>('/api/v1/ipban/hosts', { signal: opts.signal }),
  ipbanUpdateHost: (hostId: number, policy: IPBanHostPolicy) =>
    request<IPBanHostPolicy>(`/api/v1/ipban/hosts/${hostId}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(policy)
    }),
  ipbanActive: (opts: { host?: number; limit?: number; signal?: AbortSignal } = {}) => {
    const q = new URLSearchParams();
    if (opts.host) q.set('host', String(opts.host));
    if (opts.limit) q.set('limit', String(opts.limit));
    const qs = q.toString();
    return request<IPBanActive[]>(`/api/v1/ipban/active${qs ? `?${qs}` : ''}`, { signal: opts.signal });
  },
  ipbanFleet: (opts: { signal?: AbortSignal } = {}) =>
    request<IPBanFleet[]>('/api/v1/ipban/fleet', { signal: opts.signal }),
  ipbanManualBan: (body: { ip: string; ttl_s: number; note?: string }) =>
    request<IPBanFleet>('/api/v1/ipban/fleet', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }),
  ipbanFleetUnban: (ip: string) => request<void>(`/api/v1/ipban/fleet/${ip}`, { method: 'DELETE' }),
  ipbanHostUnban: (hostId: number, ip: string) =>
    request<void>('/api/v1/ipban/unban', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host_id: hostId, ip })
    }),
  ipbanEvents: (opts: { host?: number; ip?: string; limit?: number; before?: string; signal?: AbortSignal } = {}) => {
    const q = new URLSearchParams();
    if (opts.host) q.set('host', String(opts.host));
    if (opts.ip) q.set('ip', opts.ip);
    if (opts.limit) q.set('limit', String(opts.limit));
    if (opts.before) q.set('before', opts.before);
    const qs = q.toString();
    return request<IPBanEvent[]>(`/api/v1/ipban/events${qs ? `?${qs}` : ''}`, { signal: opts.signal });
  }
};
