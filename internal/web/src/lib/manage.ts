// Turning management API errors into what the dashboard tells the user.
// The server's message is already human-readable; these add what to do
// next where the code alone says more than the message does.
import { ApiError } from './api.svelte';
import { toast } from './toast.svelte';
import type { ErrorCode, ManagementErrorCode } from './types';

const hints: Partial<Record<ManagementErrorCode | ErrorCode, string>> = {
  forge_error: 'GitHub refused or failed the request.',
  installation_served: 'The App serves this account.',
  already_queued: 'That is already queued or running; it will show up here when it finishes.',
  unauthenticated: 'Your session has ended; sign in again.',
  csrf: 'The request was refused as not coming from this page; reload and try again.',
  not_cancelable: 'The review is no longer running.',
  forbidden: 'You are not allowed to do this.',
};

// describe is one line for a failed management call.
export function describe(err: unknown): string {
  if (!(err instanceof ApiError)) return err instanceof Error ? err.message : String(err);
  const hint = hints[err.code as ManagementErrorCode | ErrorCode];
  if (!hint) return err.message || `request failed (${err.status})`;
  return err.message && err.message !== hint ? `${hint} ${err.message}` : hint;
}

export function isCode(err: unknown, code: ManagementErrorCode): boolean {
  return err instanceof ApiError && err.code === code;
}

// What became of a bulk action's requests: how many were done, how many
// the server said were already under way, and each that failed, by name
// with why.
export interface Tally {
  done: number;
  already: number;
  failed: string[];
}

// sendEach sends one request per target, one at a time, and tallies them.
// The server's already_queued is not a failure: what was asked for is
// under way.
export async function sendEach<T>(targets: readonly T[], name: (t: T) => string, send: (t: T) => Promise<unknown>): Promise<Tally> {
  const tally: Tally = { done: 0, already: 0, failed: [] };
  for (const t of targets) {
    try {
      await send(t);
      tally.done++;
    } catch (err) {
      if (isCode(err, 'already_queued')) tally.already++;
      else tally.failed.push(`${name(t)}: ${describe(err)}`);
    }
  }
  return tally;
}

// toastTally reports a bulk action: done says what was done, already what
// the ones under way are called, and each failure follows.
export function toastTally(t: Tally, done: string, already = 'already queued'): void {
  const parts = [done];
  if (t.already) parts.push(`${t.already} ${already}`);
  if (t.failed.length) parts.push(`${t.failed.length} failed (${t.failed.join('; ')})`);
  toast(parts.join(', '), t.failed.length ? 'danger' : 'ok');
}
