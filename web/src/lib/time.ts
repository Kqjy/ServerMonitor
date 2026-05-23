export type RangePreset = '15m' | '1h' | '6h' | '24h' | '7d';
export type CustomRange = { fromMs: number; toMs: number };
export type Range = RangePreset | CustomRange;

export const ranges: RangePreset[] = ['15m', '1h', '6h', '24h', '7d'];

export function isPreset(r: Range): r is RangePreset {
  return typeof r === 'string';
}

export function rangeToFrom(r: Range): string {
  if (isPreset(r)) {
    switch (r) {
      case '15m': return '-15m';
      case '1h': return '-1h';
      case '6h': return '-6h';
      case '24h': return '-24h';
      case '7d': return '-168h';
    }
  }
  return new Date(r.fromMs).toISOString();
}

export function rangeToTo(r: Range): string | undefined {
  if (isPreset(r)) return undefined;
  return new Date(r.toMs).toISOString();
}

export function rangeMs(r: Range): number {
  if (isPreset(r)) {
    switch (r) {
      case '15m': return 15 * 60_000;
      case '1h':  return 60 * 60_000;
      case '6h':  return 6 * 60 * 60_000;
      case '24h': return 24 * 60 * 60_000;
      case '7d':  return 7 * 24 * 60 * 60_000;
    }
  }
  return r.toMs - r.fromMs;
}

export function rangeBoundsMs(r: Range, now: number = Date.now()): { fromMs: number; toMs: number } {
  if (isPreset(r)) {
    return { fromMs: now - rangeMs(r), toMs: now };
  }
  return { fromMs: r.fromMs, toMs: r.toMs };
}

export function rangeLabel(r: Range): string {
  if (isPreset(r)) return r;
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

