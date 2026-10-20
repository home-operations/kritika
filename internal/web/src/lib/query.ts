import { QueryClient } from '@tanstack/svelte-query';
import { getJSON } from './api.svelte';
import { accountApi } from './links';
import type { EventKind, LiveEvent, ReviewDiff } from './types';

const STALE_TIME = 30_000;

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: STALE_TIME,
      retry: false,
      networkMode: 'always',
    },
  },
});

export function apiQuery<T>(path: string) {
  return {
    queryKey: ['api', path] as const,
    queryFn: ({ signal }: { signal: AbortSignal }) => getJSON<T>(path, signal),
  };
}

function accountPath(path: string): string | undefined {
  return path.match(/^\/api\/v1\/accounts\/[^/]+\/[^/?]+/)?.[0];
}

// Which events a loaded read refreshes on, by its API path: Resource.live
// and Paged reload on one. A path with no rule refreshes on none. Cache
// staleness is wider than this (invalidateApi): a read the rule leaves out
// can still carry what the event moved, such as the spend a review embeds.
const EVERY: readonly EventKind[] = ['review', 'runner_run', 'index_run', 'followup', 'model_call'];
// Model calls only move spend and the queue; the rest is a run starting,
// progressing or ending.
const RUNS: readonly EventKind[] = ['review', 'runner_run', 'index_run', 'followup'];
const ACCOUNT = String.raw`/api/v1/accounts/[^/]+/[^/]+`;

const RULES: { path: RegExp; kinds: readonly EventKind[]; review?: boolean }[] = [
  { path: /^\/api\/v1\/accounts$/, kinds: RUNS },
  { path: /^\/api\/v1\/queue$/, kinds: EVERY },
  { path: /^\/api\/v1\/pulls$/, kinds: RUNS },
  { path: new RegExp(`^${ACCOUNT}$`), kinds: ['review', 'followup'] },
  { path: new RegExp(`^${ACCOUNT}/attention$`), kinds: ['review'] },
  { path: new RegExp(`^${ACCOUNT}/queue$`), kinds: EVERY },
  { path: new RegExp(`^${ACCOUNT}/repos$`), kinds: ['review', 'index_run'] },
  { path: new RegExp(`^${ACCOUNT}/repos/[^/]+/[^/]+$`), kinds: RUNS },
  { path: new RegExp(`^${ACCOUNT}/pulls$`), kinds: RUNS },
  { path: new RegExp(`^${ACCOUNT}/findings$`), kinds: ['review'] },
  { path: new RegExp(`^${ACCOUNT}/followups$`), kinds: ['followup'] },
  { path: new RegExp(`^${ACCOUNT}/chatgpt/allowances$`), kinds: ['model_call'] },
  // Only the events of the review the path names.
  { path: new RegExp(`^${ACCOUNT}/reviews/([^/]+)(/.*)?$`), kinds: EVERY, review: true },
];

// refreshes reports whether a read of the API path reloads on the event.
export function refreshes(path: string, e: LiveEvent): boolean {
  const account = accountPath(path);
  if (account && account !== accountApi(e.account)) return false;
  const bare = path.split('?')[0]!;
  for (const r of RULES) {
    const m = bare.match(r.path);
    if (!m) continue;
    if (!r.kinds.includes(e.kind)) return false;
    return !r.review || decodeURIComponent(m[1]!) === (e.kind === 'review' ? e.id : e.reviewId);
  }
  return false;
}

export function invalidateApi(path?: string, event?: LiveEvent): void {
  const account = path && accountPath(path);
  void queryClient.invalidateQueries({
    queryKey: ['api'],
    refetchType: 'none',
    predicate: (query) => {
      const cachedPath = query.queryKey[1] as string;
      const cachedAccount = accountPath(cachedPath);
      if (account && cachedAccount && account !== cachedAccount) return false;
      if (event) {
        const artifact = cachedPath.match(/\/reviews\/([^/]+)\/(diff|raw|transcript)$/);
        if (artifact) {
          if (artifact[1] !== (event.kind === 'review' ? event.id : event.reviewId)) return false;
          // Final diffs do not change with run events; resync still revalidates retention.
          if (artifact[2] === 'diff' && query.state.data && !(query.state.data as ReviewDiff).pending) return false;
        }
      }
      return true;
    },
  });
}
