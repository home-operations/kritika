// Client-side column sorting for a list table: the rows that the page
// loaded, in the column a header button picked. A second click reverses
// it. Empty values sort last whichever way.
export type SortDir = 'asc' | 'desc';
export type SortValue = string | number | null | undefined;

export class Sorter<K extends string, T> {
  key = $state<K | undefined>();
  dir = $state<SortDir>('asc');
  readonly #by: Record<K, (row: T) => SortValue>;

  constructor(by: Record<K, (row: T) => SortValue>) {
    this.#by = by;
  }

  toggle(key: K): void {
    if (this.key === key) {
      this.dir = this.dir === 'asc' ? 'desc' : 'asc';
    } else {
      this.key = key;
      this.dir = 'asc';
    }
  }

  // aria is the header's aria-sort for key.
  aria(key: K): 'ascending' | 'descending' | 'none' {
    if (this.key !== key) return 'none';
    return this.dir === 'asc' ? 'ascending' : 'descending';
  }

  // sort returns rows in the picked order, rows itself when none is
  // picked. The sort is stable, so equal rows keep the server's order.
  sort(rows: T[]): T[] {
    const key = this.key;
    if (key === undefined) return rows;
    const value = this.#by[key];
    const sign = this.dir === 'asc' ? 1 : -1;
    return [...rows].sort((a, b) => compareValues(value(a), value(b), sign));
  }
}

// SortControl is what a column header needs of a Sorter, whatever its rows.
export type SortControl<K extends string> = Pick<Sorter<K, unknown>, 'aria' | 'toggle'>;

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

const empty = (v: SortValue): boolean => v === null || v === undefined || v === '';

export function compareValues(a: SortValue, b: SortValue, sign: 1 | -1 = 1): number {
  if (empty(a) || empty(b)) return Number(empty(a)) - Number(empty(b));
  if (typeof a === 'number' && typeof b === 'number') return sign * (a - b);
  return sign * collator.compare(String(a), String(b));
}
