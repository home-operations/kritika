// Resource is one fetched value plus its loading/error state, so every page
// renders the same loading, error and loaded states. A load that finishes
// after a newer one started is dropped, so a slow response can't overwrite
// a fresher one.
import { ApiError, getJSON } from './api.svelte';
import { subscribe } from './events.svelte';
import type { EventKind, LiveEvent, Page } from './types';

export class Resource<T> {
  data = $state<T | undefined>(undefined);
  error = $state<Error | undefined>(undefined);
  loading = $state(false);
  #seq = 0;
  readonly #fetcher: () => Promise<T>;

  constructor(fetcher: () => Promise<T>) {
    this.#fetcher = fetcher;
  }

  async load(): Promise<void> {
    const seq = ++this.#seq;
    this.loading = true;
    try {
      const data = await this.#fetcher();
      if (seq !== this.#seq) return;
      this.data = data;
      this.error = undefined;
    } catch (err) {
      if (seq !== this.#seq) return;
      this.error = err instanceof Error ? err : new Error(String(err));
    } finally {
      if (seq === this.#seq) this.loading = false;
    }
  }
}

export function errorMessage(err: Error): string {
  return err instanceof ApiError ? `${err.message} (${err.status})` : err.message;
}

const KINDS: readonly EventKind[] = ['review', 'runner_run', 'index_run', 'followup', 'model_call'];

// What a debounced refetch was woken by: the event kinds seen since the last
// refetch, plus 'resync' when the server asked for everything.
export type Dirty = ReadonlySet<EventKind | 'resync'>;

function isLiveEvent(v: unknown): v is LiveEvent {
  return typeof v === 'object' && v !== null && 'kind' in v && 'account' in v;
}

// JOB_POLL_MS is how often a page that shows jobs refetches them: a job's
// state changes (a retry coming due, a worker picking it up) are not all
// events.
const JOB_POLL_MS = 15_000;

// pollJobs calls refetch every JOB_POLL_MS. It returns the stop, so it
// drops straight into an $effect.
export function pollJobs(refetch: () => void): () => void {
  const t = setInterval(refetch, JOB_POLL_MS);
  return () => clearInterval(t);
}

// live calls refetch whenever a server-sent event matches, and on every
// "resync" (the server lost track of what this client saw). Calls are
// debounced by delayMs, but a steady stream still refetches at least every
// maxWaitMs so the page never freezes mid-run. It returns the unsubscribe,
// so it drops straight into an $effect.
export function live(
  match: (e: LiveEvent) => boolean,
  refetch: (dirty: Dirty) => void,
  delayMs = 300,
  maxWaitMs = 3000,
): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let since = 0;
  let dirty = new Set<EventKind | 'resync'>();
  const run = () => {
    timer = undefined;
    const d = dirty;
    dirty = new Set();
    refetch(d);
  };
  const fire = (kind: EventKind | 'resync') => {
    dirty.add(kind);
    const now = Date.now();
    if (timer === undefined) since = now;
    else if (now - since >= maxWaitMs) return;
    else clearTimeout(timer);
    timer = setTimeout(run, Math.min(delayMs, Math.max(0, since + maxWaitMs - now)));
  };
  const offs = KINDS.map((k) =>
    subscribe(k, (d) => {
      if (isLiveEvent(d) && match(d)) fire(k);
    }),
  );
  offs.push(subscribe('resync', () => fire('resync')));
  return () => {
    clearTimeout(timer);
    for (const off of offs) off();
  };
}

// Paged refreshes the loaded number of pages from fresh cursors, publishing
// them together so a live update cannot mix old and new page boundaries.
// Changing the query clears its data and discards any pending Load more.
export class Paged<T> {
  readonly first: Resource<Page<T>>;
  loadingMore = $state(false);
  moreError = $state<Error | undefined>(undefined);
  #pages = 1;
  #gen = 0;
  #base: string | undefined;
  #more: Promise<void> | undefined;
  #refresh: Promise<void> | undefined;
  #refreshAgain = false;
  readonly #url: (cursor?: string) => string;
  readonly #key: (t: T) => string;

  constructor(url: (cursor?: string) => string, key: (t: T) => string) {
    this.#url = url;
    this.#key = key;
    this.first = new Resource(async () => {
      const gen = this.#gen;
      const pages = this.#pages;
      let page = await getJSON<Page<T>>(url());
      const items = [...page.items];
      for (let i = 1; i < pages && page.nextCursor; i++) {
        if (gen !== this.#gen) return page;
        page = await getJSON<Page<T>>(url(page.nextCursor));
        items.push(...page.items);
      }
      return { items: this.#unique(items), nextCursor: page.nextCursor };
    });
  }

  #unique(items: T[]): T[] {
    const seen = new Set<string>();
    return items.filter((item) => {
      const key = this.#key(item);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  }

  get cursor(): string | null {
    return this.first.data?.nextCursor ?? null;
  }

  get items(): T[] {
    return this.first.data?.items ?? [];
  }

  load(): Promise<void> {
    const base = this.#url();
    if (base !== this.#base) {
      this.#base = base;
      this.#gen++;
      this.#pages = 1;
      this.#more = undefined;
      this.#refresh = undefined;
      this.#refreshAgain = false;
      this.first.data = undefined;
      this.first.error = undefined;
      this.moreError = undefined;
      this.loadingMore = false;
    } else if (this.#more) {
      // Finish appending before refreshing, so a live event cannot lose
      // the page the user is currently asking to see.
      const gen = this.#gen;
      return this.#more.then(() => {
        if (gen === this.#gen) return this.load();
      });
    }
    if (this.#refresh) {
      this.#refreshAgain = true;
      return this.#refresh;
    }
    this.#refresh = this.#refreshPages(this.#gen);
    return this.#refresh;
  }

  // Coalesce events during a multi-page fetch without discarding its
  // result: a busy account must still make progress through live updates.
  async #refreshPages(gen: number): Promise<void> {
    do {
      this.#refreshAgain = false;
      await this.first.load();
    } while (gen === this.#gen && this.#refreshAgain);
    if (gen === this.#gen) this.#refresh = undefined;
  }

  more(): Promise<void> {
    const cursor = this.cursor;
    if (!cursor || this.first.loading || this.#more) return Promise.resolve();
    this.#more = this.#loadMore(cursor);
    return this.#more;
  }

  async #loadMore(cursor: string): Promise<void> {
    const gen = this.#gen;
    this.loadingMore = true;
    this.moreError = undefined;
    try {
      const p = await getJSON<Page<T>>(this.#url(cursor));
      if (gen !== this.#gen) return;
      this.first.data = { items: this.#unique([...this.items, ...p.items]), nextCursor: p.nextCursor };
      this.#pages++;
    } catch (err) {
      if (gen === this.#gen) this.moreError = err instanceof Error ? err : new Error(String(err));
    } finally {
      if (gen === this.#gen) {
        this.loadingMore = false;
        this.#more = undefined;
      }
    }
  }
}
