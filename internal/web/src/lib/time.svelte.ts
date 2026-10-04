// A shared reactive clock so relative timestamps ("5m ago") stay current
// without each component owning a timer. Components that read `clock.now`
// re-render when it ticks.
import { setDatePrefs, type DatePrefs } from './dates';

// rev counts the changes to how dates are written, so what shows one is
// rendered again when the viewer changes their zone or clock.
export const clock = $state({ now: Date.now(), rev: 0 });

// applyDatePrefs has every date on the page written the viewer's way.
export function applyDatePrefs(p: DatePrefs): void {
  setDatePrefs(p);
  clock.rev++;
}

export function initClock(): void {
  setInterval(() => {
    clock.now = Date.now();
  }, 30_000);
}
