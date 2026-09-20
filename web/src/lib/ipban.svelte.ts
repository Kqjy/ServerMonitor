import { api, type IPBanEvent } from './api';

export class EventPager {
  readonly pageSizes = [8, 16, 50];
  rows = $state<IPBanEvent[]>([]);
  pageSize = $state(8);
  page = $state(0);
  more = $state(false);
  loadingOlder = $state(false);
  loadError = $state<string | null>(null);
  historyCapped = $state(false);
  private host: () => number | undefined;
  private chunk: number;
  private maxRows: number;
  private generation = 0;

  constructor(opts: { host?: () => number | undefined; chunk?: number; maxRows?: number } = {}) {
    this.host = opts.host ?? (() => undefined);
    this.chunk = opts.chunk ?? 100;
    this.maxRows = opts.maxRows ?? 5000;
    this.pageSize = this.pageSizes[0];
  }

  get pageCount() {
    return Math.max(1, Math.ceil(this.rows.length / this.pageSize));
  }

  get index() {
    return Math.min(Math.max(this.page, 0), this.pageCount - 1);
  }

  get from() {
    return this.index * this.pageSize;
  }

  get to() {
    return Math.min(this.from + this.pageSize, this.rows.length);
  }

  get visible() {
    return this.rows.slice(this.from, this.to);
  }

  get atOldest() {
    return this.index >= this.pageCount - 1 && !this.more;
  }

  get refreshDepth() {
    return this.chunk;
  }

  head(rows: IPBanEvent[], depth: number) {
    if (rows.length < depth) {
      this.rows = rows;
      this.more = false;
      this.historyCapped = false;
    } else {
      const seen = new Set<number>();
      const merged = [...rows, ...this.rows]
        .sort((a, b) => b.time.localeCompare(a.time) || b.id - a.id)
        .filter((event) => !seen.has(event.id) && seen.add(event.id));
      this.rows = merged.slice(0, this.maxRows);
      if (merged.length > this.maxRows) this.historyCapped = true;
      this.more = !this.historyCapped;
    }
    this.loadError = null;
  }

  reset() {
    this.generation++;
    this.rows = [];
    this.page = 0;
    this.more = false;
    this.loadingOlder = false;
    this.loadError = null;
    this.historyCapped = false;
  }

  async loadOlder() {
    if (this.loadingOlder || !this.more) return;
    const last = this.rows[this.rows.length - 1];
    if (!last) {
      this.more = false;
      return;
    }
    const remaining = this.maxRows - this.rows.length;
    if (remaining <= 0) {
      this.more = false;
      this.historyCapped = true;
      return;
    }
    const generation = this.generation;
    const host = this.host();
    this.loadingOlder = true;
    try {
      const older = await api.ipbanEvents({
        host,
        limit: Math.min(this.chunk, remaining),
        before: last.time,
        beforeId: last.id
      });
      if (generation !== this.generation || host !== this.host()) return;
      const seen = new Set(this.rows.map((e) => e.id));
      const unique = older.filter((e) => !seen.has(e.id));
      this.rows = [...this.rows, ...unique];
      const reachedCap = this.rows.length >= this.maxRows;
      this.historyCapped = reachedCap && older.length >= Math.min(this.chunk, remaining);
      this.more = !reachedCap && unique.length > 0 && older.length >= Math.min(this.chunk, remaining);
      this.loadError = null;
    } catch (e) {
      this.loadError = (e as Error).message;
    } finally {
      if (generation === this.generation) this.loadingOlder = false;
    }
  }

  async goto(target: number) {
    const want = Math.max(target, 0);
    if ((want + 1) * this.pageSize > this.rows.length && this.more) await this.loadOlder();
    this.page = Math.min(want, this.pageCount - 1);
  }

  setPageSize(size: number) {
    this.pageSize = size;
    this.page = 0;
  }
}
