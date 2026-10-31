// A thin fetch wrapper. Every call is same-origin with credentials, and tags
// itself so that a reverse proxy or WAF can tell it apart from a browser
// navigation. The wrapper turns a non-2xx response into a typed ApiError
// instead of forcing every call site to test res.ok. A 401 means that the
// session died (expired cookie, revoked token). The wrapper then redirects to
// sign-in without a history entry, so that "back" does not bounce the user
// right back into the 401.
import { basePath } from './base';
import { parse, replace } from './router.svelte';
import { session } from './session.svelte';
import { invalidateApi, queryClient } from './query';
import type { ErrorBody } from './types';

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

const commonHeaders: HeadersInit = { 'X-Kritika': '1' };

// Where SignIn.svelte must send the user back to after signing in: the
// hash they were on when a 401 bounced them, captured before replace()
// overwrites it with #/signin. Never a sign-in route itself: a 401 while
// already on sign-in does not redirect, so returnTo keeps its '' default and
// SignIn.svelte falls back to '#/'.
export const signinState = $state<{ returnTo: string }>({ returnTo: '' });

async function toApiError(res: Response): Promise<ApiError> {
  let body: Partial<ErrorBody> = {};
  try {
    body = (await res.json()) as Partial<ErrorBody>;
  } catch {
    // No JSON body (for example, a proxy error page). Fall back to the status text.
  }
  return new ApiError(res.status, body.code ?? 'unknown', body.message ?? res.statusText);
}

// toSignIn sends the tab to sign-in and remembers where it was, unless it
// is already there. It forgets the session first: the shell sends a
// signed-in user off the sign-in page, straight back into the 401.
export function toSignIn(): void {
  session.me = undefined;
  queryClient.clear();
  if (parse(location.hash).name === 'signin') return;
  signinState.returnTo = location.hash || '#/';
  replace({ name: 'signin' });
}

async function handle<T>(res: Response): Promise<T> {
  // The dashboard has no public content. A 401 means that the session is dead
  // (expired cookie, revoked token, or no session at all). Every route then
  // redirects to sign-in, including the bare root, which has no hash. The
  // only guard is against a redirect loop when already on sign-in. The
  // router's parse decides this, so every spelling that it accepts (for
  // example, "#/signin/") counts.
  if (res.status === 401) toSignIn();
  if (!res.ok) throw await toApiError(res);
  if (res.status === 204) return undefined as T;
  try {
    return (await res.json()) as T;
  } catch {
    throw new ApiError(res.status, 'invalid_response', 'response was not valid JSON');
  }
}

export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${basePath}${path}`, {
    method: 'GET',
    signal,
    headers: commonHeaders,
    credentials: 'same-origin',
  });
  return handle<T>(res);
}

export type WriteMethod = 'POST' | 'PUT' | 'DELETE';

export async function sendJSON<T>(method: WriteMethod, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${basePath}${path}`, {
    method,
    signal,
    headers: { ...commonHeaders, 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await handle<T>(res);
  // Page callbacks coordinate list snapshots and refresh the affected views.
  if (path.startsWith('/api/')) invalidateApi(path);
  return data;
}
