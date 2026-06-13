import type { SeriesPoint } from '$lib/api';

export interface LivePoint {
  metric: string;
  labels?: Record<string, string>;
  ts: string;
  v: number;
}

type Handler = (points: LivePoint[]) => void;

interface EnvelopePoint {
  t: string;
  m: number;
  l?: Record<string, string>;
  v: number;
}

interface Envelope {
  host_id: number;
  points: EnvelopePoint[];
}

let metricNameCache: Map<number, string> | null = null;
let metricCacheFetch: Promise<void> | null = null;

function refreshMetricCache(): Promise<void> {
  metricCacheFetch ??= (async () => {
    try {
      const r = await fetch('/api/v1/metrics', { credentials: 'same-origin' });
      if (r.ok) {
        const list: { id: number; name: string }[] = await r.json();
        metricNameCache = new Map(list.map((x) => [x.id, x.name]));
      }
    } catch {
    } finally {
      metricCacheFetch = null;
    }
  })();
  return metricCacheFetch;
}

async function ensureMetricCache(): Promise<void> {
  if (metricNameCache) return;
  await refreshMetricCache();
}

function metricName(id: number): string {
  const name = metricNameCache?.get(id);
  if (name) return name;
  void refreshMetricCache();
  return '';
}

function openStream(
  url: string,
  eventName: string,
  onMessage: (evt: MessageEvent) => void,
  beforeOpen?: () => Promise<unknown>
): () => void {
  let closed = false;
  let es: EventSource | null = null;
  let retry = 1000;

  async function open() {
    if (closed) return;
    if (beforeOpen) await beforeOpen();
    if (closed) return;
    es = new EventSource(url, { withCredentials: true });
    es.addEventListener(eventName, onMessage);
    es.onerror = () => {
      es?.close();
      es = null;
      if (closed) return;
      setTimeout(open, retry);
      retry = Math.min(retry * 2, 30_000);
    };
    es.onopen = () => {
      retry = 1000;
    };
  }

  void open();
  return () => {
    closed = true;
    es?.close();
    es = null;
  };
}

export function subscribeHost(
  hostId: number,
  metricNames: string[],
  onPoints: Handler
): () => void {
  const q = new URLSearchParams();
  q.set('host_id', String(hostId));
  if (metricNames.length) q.set('metrics', metricNames.join(','));
  const url = `/api/v1/stream?${q.toString()}`;
  const wanted = new Set(metricNames);

  return openStream(
    url,
    'points',
    (evt: MessageEvent) => {
      try {
        const env = JSON.parse(evt.data) as Envelope;
        const out: LivePoint[] = [];
        for (const p of env.points) {
          const name = metricName(p.m);
          if (!name) continue;
          if (wanted.size && !wanted.has(name)) continue;
          out.push({ metric: name, labels: p.l, ts: p.t, v: p.v });
        }
        if (out.length) onPoints(out);
      } catch {}
    },
    ensureMetricCache
  );
}

type HostsHandler = (hostId: number, points: LivePoint[]) => void;

export function subscribeHosts(
  metricNames: string[],
  onPoints: HostsHandler
): () => void {
  const q = new URLSearchParams();
  q.set('host_id', '0');
  if (metricNames.length) q.set('metrics', metricNames.join(','));
  const url = `/api/v1/stream?${q.toString()}`;
  const wanted = new Set(metricNames);

  return openStream(
    url,
    'points',
    (evt: MessageEvent) => {
      try {
        const env = JSON.parse(evt.data) as Envelope;
        const out: LivePoint[] = [];
        for (const p of env.points) {
          const name = metricName(p.m);
          if (!name) continue;
          if (wanted.size && !wanted.has(name)) continue;
          out.push({ metric: name, labels: p.l, ts: p.t, v: p.v });
        }
        if (out.length) onPoints(env.host_id, out);
      } catch {}
    },
    ensureMetricCache
  );
}

export function pointsToSeries(points: LivePoint[], metric: string, labels?: Record<string, string>): SeriesPoint[] {
  return points
    .filter((p) => {
      if (p.metric !== metric) return false;
      if (labels) {
        for (const [k, v] of Object.entries(labels)) {
          if (p.labels?.[k] !== v) return false;
        }
      }
      return true;
    })
    .map((p) => ({ ts: p.ts, v: p.v }));
}

export function appendLive(arr: SeriesPoint[], point: SeriesPoint, windowMs: number): SeriesPoint[] {
  const cutoff = Date.now() - windowMs;
  const out: SeriesPoint[] = [];
  for (const p of arr) {
    if (new Date(p.ts).getTime() > cutoff) out.push(p);
  }
  out.push(point);
  return out;
}

export interface AlertEvent {
  kind: 'fire' | 'resolve';
  host_id: number;
  rule_id: number;
  rule_name: string;
  severity: string;
  metric: string;
  value: number;
  time: string;
}

export function subscribeAlerts(onAlert: (ev: AlertEvent) => void, hostId = 0): () => void {
  const q = new URLSearchParams();
  q.set('kind', 'alerts');
  q.set('host_id', String(hostId));
  return openStream(`/api/v1/stream?${q}`, 'alert', (evt: MessageEvent) => {
    try {
      onAlert(JSON.parse(evt.data) as AlertEvent);
    } catch {}
  });
}

export function rangeWindowMs(range: string): number {
  switch (range) {
    case '15m': return 15 * 60_000;
    case '1h':  return 60 * 60_000;
    case '6h':  return 6 * 60 * 60_000;
    case '24h': return 24 * 60 * 60_000;
    case '7d':  return 7 * 24 * 60 * 60_000;
  }
  return 60 * 60_000;
}
