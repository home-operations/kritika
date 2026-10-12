import type { Job } from './types';

export const waiting = (j: Job) => ['available', 'scheduled', 'retryable', 'pending'].includes(j.state);

// An overdue heartbeat is a signal to investigate, not proof that a runner
// has stopped. Its worker can recover or be rescued between queue polls.
export const overdue = (j: Job): boolean => j.heartbeatOverdue ?? false;
export const attention = (j: Job): boolean => !!j.recentFailure || j.state === 'retryable' || overdue(j);

export function waitingReason(j: Job): string {
  if (overdue(j)) return 'Worker heartbeat is overdue; waiting for recovery';
  if (j.state === 'retryable') return 'Waiting for automatic retry';
  if (j.state === 'scheduled') return 'Scheduled for a later attempt';
  if (j.state === 'available') return 'Ready for a worker';
  if (j.state === 'pending') return 'Pending promotion to the queue';
  if (j.state === 'running') return 'Worker owns this job';
  if (j.state === 'discarded') return 'Attempts exhausted or permanent failure';
  return j.state === 'cancelled' ? 'Cancellation completed' : 'Finished';
}
