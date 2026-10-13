import { QueryClient } from '@tanstack/svelte-query';
import { getJSON } from './api.svelte';
import type { LiveEvent, ReviewDiff } from './types';

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
