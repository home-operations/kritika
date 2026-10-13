import { onDestroy, untrack } from 'svelte';
import { QueryObserver, type QueryObserverResult } from '@tanstack/svelte-query';
import { queryClient, STALE_TIME } from './query';
import { session } from './session.svelte';
import { router } from './router.svelte';
import { ApiError, getJSON } from './api.svelte';
import { subscribe } from './events.svelte';
import { loadedSize, rememberSize } from './viewstate';
import type { EventKind, LiveEvent, Page } from './types';

let nextSnapshot = 0;

export class Resource<T> {
  #data = $state<T | undefined>(undefined);
  error = $state<Error | undefined>(undefined);
  loading = $state(false);
  #observer?: QueryObserver<T>;
  #disposed = false;
  #unsubscribe?: () => void;
  #loadedKey?: string;
  readonly #snapshot = ++nextSnapshot;
  readonly #fetcher: (signal: AbortSignal) => Promise<T>;
  readonly #key?: () => string;

  constructor(fetcher: (signal: AbortSignal) => Promise<T>, key?: () => string) {
    this.#fetcher = fetcher;
    this.#key = key;
    onDestroy(() => {
      this.#disposed = true;
      this.#unsubscribe?.();
    });
  }

  get data(): T | undefined { return this.#data; }

  set data(data: T | undefined) {
    this.#data = data;
    if (this.#observer && data !== undefined) queryClient.setQueryData(this.#observer.getCurrentQuery().queryKey, data);
  }

  invalidate(clear = false): void {
    if (!this.#observer) return;
    const filters = { queryKey: this.#observer.getCurrentQuery().queryKey, exact: true };
    if (clear) queryClient.removeQueries(filters);
    else void queryClient.cancelQueries(filters);
  }

  async load(): Promise<void> {
    if (this.#disposed || !session.me) return;
    const key = this.#key?.();
    await untrack(async () => {
      const changed = !this.#observer || key !== this.#loadedKey;
      this.#loadedKey = key;
      const options = {
        queryKey: this.#key ? ['api', key] : ['snapshot', this.#snapshot],
        queryFn: ({ signal }: { signal: AbortSignal }) => this.#fetcher(signal),
        // Pages decide when hidden tabs and coordinated list snapshots load.
        enabled: false,
        ...(this.#key ? {} : { gcTime: 0 }),
      };
      if (!this.#observer) {
        this.#observer = new QueryObserver<T>(queryClient, options);
        this.#unsubscribe = this.#observer.subscribe((result) => this.#apply(result));
      } else {
        this.#observer.setOptions(options);
      }
      if (changed) {
        this.data = undefined;
        this.#apply(this.#observer.getCurrentResult());
        // History restoration must settle against fresh data before restoring scroll.
        if (!router.traversal && !this.#observer.getCurrentQuery().isStaleByTime(STALE_TIME)) return;
      }
      await this.#observer.refetch();
    });
  }

  #apply(result: QueryObserverResult<T>): void {
    if (result.data !== undefined || !result.isError) this.#data = result.data;
    this.error = result.error ?? undefined;
    this.loading = result.isFetching;
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

// Live refreshes publish a coherent snapshot from fresh cursors. Initial
// restoration reveals pages as they arrive so a deep list stays usable.
export class Paged<T> {
  readonly first: Resource<Page<T>>;
  loadingMore = $state(false);
  moreError = $state<Error | undefined>(undefined);
  #loaded = 0;
  #entry = '';
  #gen = 0;
  #revision = 0;
  #base: string | undefined;
  #more: Promise<void> | undefined;
  #afterMore: Promise<void> | undefined;
  #refresh: Promise<void> | undefined;
  #refreshAgain = false;
  readonly #url: (cursor?: string) => string;
  readonly #key: (t: T) => string;

  constructor(url: (cursor?: string) => string, key: (t: T) => string) {
    this.#url = url;
    this.#key = key;
    this.first = new Resource(async (signal) => {
      const gen = this.#gen;
      const revision = this.#revision;
      const target = this.#loaded;
      const progressive = untrack(() => this.first.data === undefined);
      let page = await getJSON<Page<T>>(this.#pageURL(undefined, target || undefined), signal);
      const items = [...page.items];
      while (items.length < target && page.nextCursor) {
        if (gen !== this.#gen || revision !== this.#revision) return page;
        if (progressive) this.first.data = { items: this.#unique(items), nextCursor: page.nextCursor };
        page = await getJSON<Page<T>>(this.#pageURL(page.nextCursor, target - items.length), signal);
        items.push(...page.items);
      }
      const unique = this.#unique(items);
      if (gen === this.#gen && revision === this.#revision) {
        this.#loaded = unique.length;
        if (this.#base) rememberSize(this.#entry, this.#base, unique.length);
      }
      return { items: unique, nextCursor: page.nextCursor };
    });
  }

  #pageURL(cursor?: string, limit?: number): string {
    const url = this.#url(cursor);
    if (limit === undefined) return url;
    const [path, query] = url.split('?');
    const params = new URLSearchParams(query);
    // The list endpoints cap each response at 200 rows.
    const defaultLimit = Number(params.get('limit') ?? 50);
    params.set('limit', String(Math.min(Math.max(limit, defaultLimit), 200)));
    return path + '?' + params;
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

  // Explicit loads discard a snapshot started before a user action.
  load(): Promise<void> {
    return this.#load(false);
  }

  // Live refreshes coalesce without starving a slow snapshot.
  refresh(): Promise<void> {
    return this.#load(true);
  }

  #load(background: boolean): Promise<void> {
    const base = this.#url();
    const entry = router.entry;
    if (base !== this.#base || entry !== this.#entry) {
      this.#entry = entry;
      this.#base = base;
      this.#gen++;
      this.#revision++;
      this.first.invalidate(true);
      this.#loaded = untrack(() => router.traversal) ? loadedSize(entry, base) : 0;
      this.#more = undefined;
      this.#afterMore = undefined;
      this.#refresh = undefined;
      this.#refreshAgain = false;
      this.first.data = undefined;
      this.first.error = undefined;
      this.moreError = undefined;
      this.loadingMore = false;
    } else if (!background && this.#refresh) {
      this.#revision++;
      this.first.invalidate();
    }
    if (this.#more) {
      if (!this.#afterMore) {
        const gen = this.#gen;
        this.#afterMore = this.#more.then(() => {
          if (gen !== this.#gen) return;
          this.#afterMore = undefined;
          return this.refresh();
        });
      }
      return this.#afterMore;
    }
    if (this.#refresh) {
      this.#refreshAgain = true;
      return this.#refresh;
    }
    this.#refresh = this.#refreshPages(this.#gen);
    return this.#refresh;
  }

  async #refreshPages(gen: number): Promise<void> {
    let revision: number;
    do {
      revision = this.#revision;
      this.#refreshAgain = false;
      await this.first.load();
      // A queued append takes the next turn, even during a steady stream.
    } while (gen === this.#gen && (revision !== this.#revision || (this.#refreshAgain && !this.#more)));
    if (gen === this.#gen) this.#refresh = undefined;
  }

  more(): Promise<void> {
    if (!this.cursor || this.#more) return this.#more ?? Promise.resolve();
    this.loadingMore = true;
    this.moreError = undefined;
    this.#more = this.#loadMore(this.#gen);
    return this.#more;
  }

  async #loadMore(gen: number): Promise<void> {
    try {
      if (this.#refresh) await this.#refresh;
      if (gen !== this.#gen || !this.cursor) return;
      const p = await getJSON<Page<T>>(this.#url(this.cursor));
      if (gen !== this.#gen) return;
      this.first.data = { items: this.#unique([...this.items, ...p.items]), nextCursor: p.nextCursor };
      this.#loaded = this.items.length;
      if (this.#base) rememberSize(this.#entry, this.#base, this.#loaded);
    } catch (err) {
      if (gen === this.#gen) this.moreError = err instanceof Error ? err : new Error(String(err));
    } finally {
      if (gen === this.#gen) {
        this.loadingMore = false;
        this.#more = undefined;
        if (this.#refreshAgain && !this.#afterMore) void this.refresh();
      }
    }
  }
}
