// A shared reactive clock so relative timestamps ("5m ago") stay current
// without each component owning a timer. Components that read `clock.now`
// re-render when it ticks.

export const clock = $state({ now: Date.now() });

export function initClock(): void {
  setInterval(() => {
    clock.now = Date.now();
  }, 30_000);
}

// timeAgo renders an ISO timestamp as a compact relative string against `now`
// (pass clock.now so it updates live): "5m ago", or "in 5m" for one still
// to come. Empty/invalid input yields "".
export function timeAgo(iso: string | undefined, now: number): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.round(Math.abs(now - t) / 1000);
  if (s < 45) return 'just now';
  const say = (n: string) => (t > now ? `in ${n}` : `${n} ago`);
  const m = Math.round(s / 60);
  if (m < 60) return say(`${m}m`);
  const h = Math.round(m / 60);
  if (h < 24) return say(`${h}h`);
  const d = Math.round(h / 24);
  if (d < 30) return say(`${d}d`);
  const mo = Math.round(d / 30);
  if (mo < 12) return say(`${mo}mo`);
  return say(`${Math.round(mo / 12)}y`);
}

// absolute renders the full local timestamp for a tooltip.
export function absolute(iso: string | undefined): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  return Number.isNaN(t) ? '' : new Date(t).toLocaleString();
}
