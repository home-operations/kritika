// Server-sent events keep the dashboard live: run status changes, review
// completions, follow-up updates, and the like. One shared EventSource per
// page load, reconnected with the same exponential-backoff-with-jitter
// that konflate's websocket uses. A server restart then does not reconnect
// every open tab in lockstep a couple seconds later.
import { toSignIn } from './api.svelte';
import { basePath } from './base';
import { invalidateApi } from './query';
import { accountApi } from './links';
import type { EventKind, LiveEvent, Resync } from './types';

type Listener = (data: unknown) => void;

export const EVENT_KINDS: readonly EventKind[] = ['review', 'runner_run', 'index_run', 'followup', 'model_call'];
const listeners = new Map<string, Set<Listener>>(EVENT_KINDS.map((kind) => [kind, new Set()]));
let source: EventSource | null = null;
let attempt = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
let entry = '';

// bootedFrom records the URL of the script this page booted from. main.ts,
// the bundle's entry module, passes its own import.meta.url: Vite names the
// file after the content of the whole bundle, so it says which build of the
// dashboard this tab runs.
export function bootedFrom(url: string): void {
  entry = url;
}

// stream is what the topbar says of the connection. It only reads as down
// once the stream stays closed for DOWN_AFTER_MS. The routine reconnect
// after a server restart or a proxy's idle cut then goes unremarked.
export const stream = $state<{ down: boolean; since: string }>({ down: false, since: '' });
const DOWN_AFTER_MS = 2_000;
let downTimer: ReturnType<typeof setTimeout> | undefined;

function clearDown(): void {
  clearTimeout(downTimer);
  downTimer = undefined;
  stream.down = false;
}

function armDown(): void {
  if (downTimer !== undefined || stream.down) return;
  const since = new Date().toISOString();
  downTimer = setTimeout(() => {
    downTimer = undefined;
    stream.down = true;
    stream.since = since;
  }, DOWN_AFTER_MS);
}

function parseData(e: MessageEvent<string>): unknown {
  try {
    return JSON.parse(e.data) as unknown;
  } catch {
    return e.data;
  }
}

function dispatch(kind: string, e: MessageEvent<string>): void {
  const data = parseData(e);
  if (typeof data === 'object' && data !== null && 'account' in data && typeof data.account === 'string') {
    invalidateApi(accountApi(data.account), data as LiveEvent);
  }
  for (const fn of listeners.get(kind) ?? []) fn(data);
}

// attachKind wires one server-sent event "kind" (the `event:` field) to
// dispatch. Called once per kind per connection: on a fresh subscribe if
// already connected, and for every known kind when a new EventSource opens
// after a reconnect. connect() wires resync itself.
function attachKind(kind: string): void {
  if (kind === 'resync') return;
  source?.addEventListener(kind, (e) => dispatch(kind, e as MessageEvent<string>));
}

function isResync(v: unknown): v is Resync {
  return typeof v === 'object' && v !== null && typeof (v as Resync).entry === 'string';
}

// runsOtherBuild reports whether a resync names an entry script other than
// the one this page booted from. A resync that names none is no reason to
// reload. Such a resync comes from a server whose UI was built without the
// manifest, or from a release before resyncs named one.
function runsOtherBuild(data: unknown): boolean {
  if (!entry || !isResync(data) || !data.entry) return false;
  return new URL(data.entry, document.baseURI).href !== entry;
}

function scheduleReconnect(): void {
  // delay ∈ [base/2, base), base = 1s·2^attempt capped at 30s. Every client
  // otherwise reconnects in lockstep shortly after a server restart.
  const base = Math.min(30_000, 1_000 * 2 ** attempt);
  attempt++;
  timer = setTimeout(connect, base / 2 + Math.random() * (base / 2));
}

// probeSession asks /api/v1/me whether the session outlived the stream: an
// EventSource cannot see the 401 that refused it, so without this a dead
// session reconnects forever.
async function probeSession(): Promise<void> {
  let res: Response;
  try {
    res = await fetch(`${basePath}/api/v1/me`, { headers: { 'X-Kritika': '1' }, credentials: 'same-origin' });
  } catch {
    return;
  }
  if (res.status !== 401) return;
  closeEvents();
  toSignIn();
}

function connect(): void {
  source = new EventSource(`${basePath}/api/events`);
  source.addEventListener('open', () => {
    attempt = 0;
    clearDown();
  });
  // The server opens every stream with a resync, since anything published
  // while the stream was down, or before it first opened, never reached it.
  // The server sends another when the stream falls behind. A resync that names
  // another build means that the server was upgraded under this tab, whose
  // code can fail to fit its API. The tab reloads rather than refetch.
  source.addEventListener('resync', (e) => {
    const data = parseData(e as MessageEvent<string>);
    if (runsOtherBuild(data)) {
      closeEvents();
      location.reload();
      return;
    }
    invalidateApi();
    for (const fn of listeners.get('resync') ?? []) fn(data);
  });
  // EventSource retries on its own after 'error', but only at a fixed
  // interval. Close it and drive the reconnect ourselves so that the backoff
  // above actually applies.
  source.addEventListener('error', () => {
    source?.close();
    armDown();
    // Once the stream fails twice running, make sure that it is not the session.
    if (attempt > 0) void probeSession();
    scheduleReconnect();
  });
  for (const kind of listeners.keys()) attachKind(kind);
}

export function initEvents(): void {
  if (source) return;
  armDown();
  connect();
}

// closeEvents tears down the shared connection on sign-out, so a stale
// session cookie does not keep streaming another account's events into a
// signed-out tab. initEvents() reconnects cleanly on the next sign-in.
export function closeEvents(): void {
  clearTimeout(timer);
  timer = undefined;
  source?.close();
  source = null;
  attempt = 0;
  clearDown();
}

// subscribe registers fn for events of the given kind and returns an
// unsubscribe function. Safe to call before initEvents(): the listener is
// attached to the EventSource once one exists (immediately, or on the next
// connect).
export function subscribe(kind: string, fn: Listener): () => void {
  let set = listeners.get(kind);
  if (!set) {
    set = new Set();
    listeners.set(kind, set);
    attachKind(kind);
  }
  set.add(fn);
  return () => set.delete(fn);
}
