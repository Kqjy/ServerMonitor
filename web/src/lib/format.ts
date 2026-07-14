export function pct(v: number, digits = 1): string {
  return `${v.toFixed(digits)}%`;
}

export function autoDigits(magnitude: number): number {
  const m = Math.abs(magnitude);
  if (m < 1) return 2;
  if (m < 10) return 1;
  return 0;
}

export function tickDigits(ticks: ArrayLike<number>): number {
  if (ticks.length < 2) return 0;
  const step = Math.abs(ticks[1] - ticks[0]);
  if (step === 0) return 0;
  return Math.max(0, Math.ceil(-Math.log10(step)));
}

export function bytes(n: number, digits = 1): string {
  if (!isFinite(n)) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(digits)} ${units[i]}`;
}

export function dur(secs: number): string {
  if (secs < 60) return `${secs.toFixed(0)}s`;
  if (secs < 3600) return `${(secs / 60).toFixed(0)}m`;
  if (secs < 86400) return `${(secs / 3600).toFixed(1)}h`;
  return `${(secs / 86400).toFixed(1)}d`;
}

export function timeAgo(iso?: string): string {
  if (!iso) return 'never';
  const ms = Date.now() - new Date(iso).getTime();
  if (ms < 0) return 'just now';
  if (ms < 60_000) return `${Math.floor(ms / 1000)}s ago`;
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ago`;
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h ago`;
  return `${Math.floor(ms / 86_400_000)}d ago`;
}

export function timeUntil(iso?: string): string {
  if (!iso) return '';
  const ms = new Date(iso).getTime() - Date.now();
  if (isNaN(ms)) return '';
  if (ms <= 0) return 'due now';
  if (ms < 60_000) return `in ${Math.floor(ms / 1000)}s`;
  if (ms < 3_600_000) return `in ${Math.floor(ms / 60_000)}m`;
  if (ms < 86_400_000) return `in ${Math.floor(ms / 3_600_000)}h`;
  return `in ${Math.floor(ms / 86_400_000)}d`;
}

export function statusFor(iso?: string, intervalS = 10): 'good' | 'warn' | 'bad' | 'idle' {
  if (!iso) return 'idle';
  const sec = (Date.now() - new Date(iso).getTime()) / 1000;
  if (sec <= intervalS * 3) return 'good';
  if (sec <= intervalS * 12) return 'warn';
  return 'bad';
}

export type Severity = 'info' | 'warning' | 'critical';

export const severityRank: Record<string, number> = { critical: 3, warning: 2, info: 1 };

type SeverityVariant = 'chip' | 'banner' | 'row' | 'pill';

const severityClassMap: Record<SeverityVariant, Record<string, string>> = {
  chip: {
    critical: 'border-rose-500/40 bg-rose-500/15 text-rose-300',
    warning: 'border-amber-500/40 bg-amber-500/15 text-amber-300',
    info: 'border-sky-500/40 bg-sky-500/15 text-sky-300'
  },
  banner: {
    critical: 'border-rose-500/40 bg-rose-500/10 text-rose-200',
    warning: 'border-amber-500/40 bg-amber-500/10 text-amber-200',
    info: 'border-sky-500/40 bg-sky-500/10 text-sky-200'
  },
  row: {
    critical: 'text-rose-300',
    warning: 'text-amber-300',
    info: 'text-sky-300'
  },
  pill: {
    critical: 'border-rose-900/60 bg-rose-950/40 text-rose-300',
    warning: 'border-amber-900/60 bg-amber-950/40 text-amber-300',
    info: 'border-sky-900/60 bg-sky-950/40 text-sky-300'
  }
};

export function severityClass(sev: string, variant: SeverityVariant): string {
  const fallbackSev = severityRank[sev] ? sev : 'info';
  return severityClassMap[variant][fallbackSev];
}
