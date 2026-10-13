import { QueryClient } from '@tanstack/svelte-query';

export const STALE_TIME = 30_000;

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: STALE_TIME,
      retry: false,
    },
  },
});
