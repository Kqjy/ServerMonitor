import type { SeriesEntry, SeriesGroupResp, SeriesPoint } from '$lib/api';

function pointMs(point: SeriesPoint): number {
  return Date.parse(point.ts);
}

export function groupPoints(response: SeriesGroupResp, metric: string): SeriesPoint[] {
  return response.metrics[metric]?.series[0]?.points ?? [];
}

export function groupSeries(response: SeriesGroupResp, metric: string): SeriesEntry[] {
  return response.metrics[metric]?.series ?? [];
}

export function mergePoints(
  current: SeriesPoint[],
  incoming: SeriesPoint[],
  fromMs: number,
  toMs: number,
  replaceFromMs?: number
): SeriesPoint[] {
  const byTime = new Map<number, SeriesPoint>();
  for (const point of current) {
    const at = pointMs(point);
    if (at >= fromMs && at <= toMs && (replaceFromMs === undefined || at < replaceFromMs)) {
      byTime.set(at, point);
    }
  }
  for (const point of incoming) {
    const at = pointMs(point);
    if (at >= fromMs && at <= toMs) byTime.set(at, point);
  }
  return Array.from(byTime.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([, point]) => point);
}

function labelsKey(labels: Record<string, string>, splitBy?: string): string {
  if (splitBy) return labels[splitBy] ?? '';
  return Object.keys(labels)
    .sort()
    .map((key) => `${key}=${labels[key]}`)
    .join(';');
}

export function mergeSeries(
  current: SeriesEntry[],
  incoming: SeriesEntry[],
  fromMs: number,
  toMs: number,
  splitBy?: string,
  replaceFromMs?: number
): SeriesEntry[] {
  const order: string[] = [];
  const byLabels = new Map<string, SeriesEntry>();

  for (const entry of current) {
    const key = labelsKey(entry.labels, splitBy);
    if (!byLabels.has(key)) order.push(key);
    byLabels.set(key, {
      labels: entry.labels,
      points: mergePoints(entry.points, [], fromMs, toMs, replaceFromMs)
    });
  }
  for (const entry of incoming) {
    const key = labelsKey(entry.labels, splitBy);
    const existing = byLabels.get(key);
    if (!existing) order.push(key);
    byLabels.set(key, {
      labels: entry.labels,
      points: mergePoints(existing?.points ?? [], entry.points, fromMs, toMs)
    });
  }
  return order
    .map((key) => byLabels.get(key)!)
    .filter((entry) => entry.points.length > 0);
}

export function incrementalFromMs(stepSec: number, nowMs: number): number {
  const stepMs = Math.max(stepSec, 1) * 1_000;
  const from = nowMs - Math.max(2 * 60_000, stepMs * 3);
  return Math.floor(from / stepMs) * stepMs;
}
