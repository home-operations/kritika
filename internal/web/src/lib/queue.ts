import type { Job } from './types';

export const waiting = (j: Job) => ['available', 'scheduled', 'retryable', 'pending'].includes(j.state);

// An overdue heartbeat is a signal to investigate, not proof that a runner
// has stopped. Its worker can recover or be rescued between queue polls.
export function overdue(j: Job, now: number): boolean {
  return j.state === 'running' && !!j.attemptedAt &&
    now - Math.max(Date.parse(j.heartbeatAt ?? j.attemptedAt), Date.parse(j.attemptedAt)) > 180_000;
}

export function waitingReason(j: Job, now: number): string {
  if (overdue(j, now)) return 'No worker heartbeat for over 3 minutes';
  if (j.state === 'retryable') return 'Waiting for automatic retry';
  if (j.state === 'scheduled') return 'Scheduled for a later attempt';
  if (j.state === 'available') return 'Ready for a worker';
  if (j.state === 'pending') return 'Pending promotion to the queue';
  if (j.state === 'running') return 'Worker owns this job';
  if (j.state === 'discarded') return 'Attempts exhausted or permanent failure';
  return j.state === 'cancelled' ? 'Cancellation completed' : 'Finished';
}
