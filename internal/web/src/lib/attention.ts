// What wants a look in an account, as its Analytics page and the
// instance's Overview both say it. Rune-free so tests can import it.
import type { Attention, MonthUsage } from './types';
import type { PullFilter } from './routes';
import { wholeNumber, type Tone } from './format';

// The open pull requests that want a look, by why, each with the pull
// request list's filter that shows exactly them.
export const WANTS: readonly { key: keyof Attention; tone: Tone; label: string; why: string; filter: PullFilter }[] = [
  { key: 'failed', tone: 'danger', label: 'failed', why: 'whose last review failed', filter: { outcome: 'failed' } },
  { key: 'capped', tone: 'warn', label: 'capped', why: 'whose last review hit a limit', filter: { outcome: 'capped' } },
  { key: 'p0', tone: 'danger', label: 'P0', why: 'whose last review found a P0', filter: { is: 'p0' } },
  { key: 'paused', tone: 'muted', label: 'paused', why: 'whose automatic reviews are paused', filter: { is: 'paused' } },
];

// A cap is close from nine tenths of it, where the Spend page's meter
// turns red.
const NEAR = 0.9;

// nearCaps says each of the account's caps that is close or reached.
export function nearCaps(u: MonthUsage): string[] {
  const out: string[] = [];
  if (u.tokensPerMonth && u.tokens >= u.tokensPerMonth * NEAR) {
    out.push(`${Math.floor((u.tokens / u.tokensPerMonth) * 100)}% of the month's tokens are spent`);
  }
  if (u.reviewsPerDay && u.reviewsToday >= u.reviewsPerDay * NEAR) {
    out.push(`${wholeNumber(u.reviewsToday)} of today's ${wholeNumber(u.reviewsPerDay)} reviews are done`);
  }
  return out;
}
