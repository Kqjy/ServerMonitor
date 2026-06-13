export class TableSort<K extends string> {
  key: K;
  dir: 'asc' | 'desc';

  constructor(initial: K) {
    this.key = $state(initial);
    this.dir = $state('desc');
  }

  toggle(k: K) {
    if (this.key === k) this.dir = this.dir === 'asc' ? 'desc' : 'asc';
    else {
      this.key = k;
      this.dir = 'desc';
    }
  }

  indicator(k: K): string {
    return this.key === k ? (this.dir === 'asc' ? ' ↑' : ' ↓') : '';
  }

  ariaSort(k: K | undefined): 'ascending' | 'descending' | undefined {
    if (k === undefined || this.key !== k) return undefined;
    return this.dir === 'asc' ? 'ascending' : 'descending';
  }

  apply<T>(rows: T[], value: (row: T) => number | string, tieBreak?: (a: T, b: T) => number): T[] {
    const flip = this.dir === 'asc' ? 1 : -1;
    const keyed = rows.map((row) => ({ row, v: value(row) }));
    keyed.sort((a, b) => {
      const cmp =
        typeof a.v === 'number' && typeof b.v === 'number'
          ? a.v - b.v
          : String(a.v).localeCompare(String(b.v));
      if (cmp !== 0) return flip * cmp;
      return tieBreak ? tieBreak(a.row, b.row) : 0;
    });
    return keyed.map((e) => e.row);
  }
}

export interface SortColumn<K extends string> {
  key?: K;
  label: string;
  cls: string;
}
