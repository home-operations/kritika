// A tiny hash router so every dashboard view is deep-linkable and survives
// refresh / browser back. Every account-scoped route carries the account's
// slug (from Me.accounts[].slug) so switching accounts is just a URL rewrite.
//
// `Route`/`REVIEW_TABS`/`parse`/`href` are pure and live in routes.ts (no
// runes, so they're importable outside Svelte's compiler -- see
// tests/routes.spec.ts for the round-trip and malformed-hash coverage).
// This file is just the `$state` wiring around them, which is why it needs
// the .svelte.ts extension.

export { REVIEW_TABS, parse, href } from './routes';
export type { Route, ReviewTab } from './routes';

import { href, parse, type Route } from './routes';

const newEntry = (): string => `${Date.now()}:${Math.random()}`;

function entryID(): string {
  const saved = history.state?.kritikaEntry;
  if (typeof saved === 'string') return saved;
  const id = newEntry();
  history.replaceState({ ...history.state, kritikaEntry: id }, '');
  return id;
}

export const router = $state({ route: parse(location.hash), entry: entryID(), traversal: false });

export function navigate(to: Route): void {
  const next = href(to);
  if (location.hash === next) {
    router.route = to;
  } else {
    history.pushState({ kritikaEntry: newEntry() }, '', next);
    router.entry = entryID();
    router.traversal = false;
    router.route = to;
  }
}

// replace preserves the history entry while synchronizing in-page filters.
export function replace(to: Route): void {
  router.traversal = false;
  router.route = to;
  try {
    history.replaceState(history.state, '', href(to));
  } catch {
    // Safari rate-limits replaceState (SecurityError past ~100 calls/30s).
    // The in-memory route is already updated; the URL syncs on the next call.
  }
}

export function initRouter(): void {
  const changed = () => {
    const existing = typeof history.state?.kritikaEntry === 'string';
    const entry = entryID();
    if (entry === router.entry && href(router.route) === href(parse(location.hash))) return;
    router.entry = entry;
    router.traversal = existing;
    router.route = parse(location.hash);
  };
  window.addEventListener('popstate', changed);
  window.addEventListener('hashchange', changed);
}
