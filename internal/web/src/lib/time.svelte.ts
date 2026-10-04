// A shared reactive clock so relative timestamps ("5m ago") stay current
// without each component owning a timer. Components that read `clock.now`
// re-render when it ticks.

export const clock = $state({ now: Date.now() });

export function initClock(): void {
  setInterval(() => {
    clock.now = Date.now();
  }, 30_000);
}
