export const PRESETS = [
  { id: '5m',  label: 'Last 5 minutes',  ms: 5 * 60_000 },
  { id: '15m', label: 'Last 15 minutes', ms: 15 * 60_000 },
  { id: '30m', label: 'Last 30 minutes', ms: 30 * 60_000 },
  { id: '1h',  label: 'Last 1 hour',     ms: 60 * 60_000 },
  { id: '3h',  label: 'Last 3 hours',    ms: 3 * 60 * 60_000 },
  { id: '6h',  label: 'Last 6 hours',    ms: 6 * 60 * 60_000 },
  { id: '12h', label: 'Last 12 hours',   ms: 12 * 60 * 60_000 },
  { id: '24h', label: 'Last 24 hours',   ms: 24 * 60 * 60_000 },
  { id: '2d',  label: 'Last 2 days',     ms: 2 * 24 * 60 * 60_000 },
  { id: '7d',  label: 'Last 7 days',     ms: 7 * 24 * 60 * 60_000 },
  { id: '30d', label: 'Last 30 days',    ms: 30 * 24 * 60 * 60_000 },
] as const;

export type RangePreset = (typeof PRESETS)[number]['id'];
export type CustomRange = { fromMs: number; toMs: number };
export type Range = RangePreset | CustomRange;

const presetById = new Map<RangePreset, (typeof PRESETS)[number]>(
  PRESETS.map((p) => [p.id, p] as const)
);

export const ranges: RangePreset[] = PRESETS.map((p) => p.id);

export function isPreset(r: Range): r is RangePreset {
  return typeof r === 'string';
}

export function rangeMs(r: Range): number {
  if (isPreset(r)) return presetById.get(r)?.ms ?? 60 * 60_000;
  return r.toMs - r.fromMs;
}

export function rangeToFrom(r: Range): string {
  if (isPreset(r)) return `-${rangeMs(r) / 1000}s`;
  return new Date(r.fromMs).toISOString();
}

export function rangeToTo(r: Range): string | undefined {
  if (isPreset(r)) return undefined;
  return new Date(r.toMs).toISOString();
}

export function rangeBoundsMs(r: Range, now: number = Date.now()): { fromMs: number; toMs: number } {
  if (isPreset(r)) {
    return { fromMs: now - rangeMs(r), toMs: now };
  }
  return { fromMs: r.fromMs, toMs: r.toMs };
}

export function presetLabel(id: RangePreset): string {
  return presetById.get(id)?.label ?? id;
}

export function rangeLabel(r: Range): string {
  if (isPreset(r)) return presetLabel(r);
  const f = new Date(r.fromMs);
  const t = new Date(r.toMs);
  return `${fmt(f)} – ${fmt(t, f)}`;
}

function fmt(d: Date, ref?: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  const sameDay = ref && d.toDateString() === ref.toDateString();
  const ymd = sameDay ? '' : `${pad(d.getMonth() + 1)}/${pad(d.getDate())} `;
  return `${ymd}${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function rangeEquals(a: Range, b: Range): boolean {
  if (isPreset(a) && isPreset(b)) return a === b;
  if (!isPreset(a) && !isPreset(b)) return a.fromMs === b.fromMs && a.toMs === b.toMs;
  return false;
}

const RANGE_STORAGE_KEY = 'sm_range';

export function loadRange(searchParams?: URLSearchParams): Range {
  if (searchParams) {
    const r = searchParams.get('range');
    if (r && (ranges as string[]).includes(r)) return r as RangePreset;
    const fromS = searchParams.get('from');
    const toS = searchParams.get('to');
    if (fromS && toS) {
      const fromMs = Date.parse(fromS);
      const toMs = Date.parse(toS);
      if (!isNaN(fromMs) && !isNaN(toMs) && toMs > fromMs) {
        return { fromMs, toMs };
      }
    }
  }
  if (typeof localStorage !== 'undefined') {
    try {
      const stored = localStorage.getItem(RANGE_STORAGE_KEY);
      if (stored && (ranges as string[]).includes(stored)) return stored as RangePreset;
    } catch {}
  }
  return '1h';
}

export function saveRange(r: Range): void {
  if (!isPreset(r)) return;
  savePresetWin(RANGE_STORAGE_KEY, r);
}

export function writeRangeToUrl(r: Range): void {
  if (typeof window === 'undefined') return;
  const u = new URL(window.location.href);
  u.searchParams.delete('range');
  u.searchParams.delete('from');
  u.searchParams.delete('to');
  if (isPreset(r)) {
    u.searchParams.set('range', r);
  } else {
    u.searchParams.set('from', new Date(r.fromMs).toISOString());
    u.searchParams.set('to', new Date(r.toMs).toISOString());
  }
  history.replaceState(history.state, '', u.toString());
}

export function loadPresetWin<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  if (typeof localStorage === 'undefined') return fallback;
  try {
    const stored = localStorage.getItem(key);
    if (stored && (allowed as readonly string[]).includes(stored)) return stored as T;
  } catch {}
  return fallback;
}

export function savePresetWin(key: string, value: string): void {
  if (typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(key, value);
  } catch {}
}

const stepBuckets = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600];

export function chooseStepSec(durationMs: number, intervalS: number): number {
  if (!(intervalS > 0)) intervalS = 10;
  let raw = Math.floor(durationMs / 1000 / 720);
  if (raw < intervalS) raw = intervalS;
  for (const b of stepBuckets) {
    if (b >= raw) return b;
  }
  return stepBuckets[stepBuckets.length - 1];
}

