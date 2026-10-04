// How the dashboard writes dates and times, in one place so every page
// reads the same. Pure and rune-free, so tests import it directly.
//
//   An instant (when a review started, a job is due): relative while it is
//   within a week either way, "5m ago" or "in 5m"; beyond that its date,
//   "Sep 1", with the year when it is not this one. Always through
//   components/Time.svelte, which carries the full timestamp on hover.
//
//   A full timestamp (a hover title, running text that needs the moment):
//   "Sep 1, 2026, 14:05:09 GMT+2", or "2:05:09 PM", in the viewer's time
//   zone and on the clock their browser's locale keeps, 12 or 24 hours.
//
//   A day a figure is counted over (a chart column, a table row): the
//   server's days are UTC, so "Sep 1, 2026" in UTC; "Sep 1" where the year
//   only crowds, as on a chart's axis.
//
// The words are English like the rest of the dashboard, whatever the
// browser's locale. How long something took is format.ts's duration.

// hour12Of is whether a locale, the browser's when none is given, keeps a
// 12-hour clock.
export function hour12Of(locale?: string): boolean {
  const cycle = new Intl.DateTimeFormat(locale, { hour: 'numeric' }).resolvedOptions().hourCycle;
  return cycle === 'h11' || cycle === 'h12';
}

function stampFormat(hour12: boolean): Intl.DateTimeFormat {
  return new Intl.DateTimeFormat('en', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: hour12 ? 'numeric' : '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: hour12 ? 'h12' : 'h23',
    timeZoneName: 'short',
  });
}
const stampFmt = stampFormat(hour12Of());
const dateFmt = new Intl.DateTimeFormat('en', { month: 'short', day: 'numeric' });
const dateYearFmt = new Intl.DateTimeFormat('en', { year: 'numeric', month: 'short', day: 'numeric' });
const dayFmt = new Intl.DateTimeFormat('en', { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' });
const dayShortFmt = new Intl.DateTimeFormat('en', { month: 'short', day: 'numeric', timeZone: 'UTC' });

const WEEK = 7 * 86_400;

// relative renders an instant against now (pass clock.now so it stays
// current). Empty or invalid input yields "".
export function relative(iso: string | null | undefined, now: number): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.round(Math.abs(now - t) / 1000);
  if (s < 45) return 'just now';
  if (s >= WEEK) return (new Date(t).getFullYear() === new Date(now).getFullYear() ? dateFmt : dateYearFmt).format(t);
  const say = (n: string) => (t > now ? `in ${n}` : `${n} ago`);
  const m = Math.round(s / 60);
  if (m < 60) return say(`${m}m`);
  const h = Math.round(m / 60);
  if (h < 24) return say(`${h}h`);
  return say(`${Math.round(h / 24)}d`);
}

// timestamp renders an instant in full. Empty or invalid input yields "".
export function timestamp(iso: string | null | undefined): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  return Number.isNaN(t) ? '' : stampFmt.format(t);
}

// day renders a UTC day the server keys a figure by, "2026-09-01"; a key
// that is not a day is returned as it is.
export function day(key: string, short = false): string {
  const t = Date.parse(`${key}T00:00:00Z`);
  return Number.isNaN(t) ? key : (short ? dayShortFmt : dayFmt).format(t);
}
