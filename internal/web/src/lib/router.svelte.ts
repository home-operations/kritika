// A tiny hash router so every dashboard view is deep-linkable and survives
// refresh / browser back. Every account-scoped route carries the account's
// slug (from Me.accounts[].slug) so switching accounts is just a URL rewrite.
//
// `Route`/`REVIEW_TABS`/`parse`/`href` are pure and live in routes.ts. They
// have no runes, so they are importable outside Svelte's compiler. See
// tests/routes.spec.ts for the round-trip and malformed-hash coverage.
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

interface ListOrigin { hash: string; label: string; depth: number }

export const router = $state({ route: parse(location.hash), entry: entryID(), traversal: false, origin: history.state?.kritikaOrigin as ListOrigin | undefined });

function originFor(to: Route): ListOrigin | undefined {
  if (to.name !== 'pull' && to.name !== 'review') return;
  const from = router.route;
  if (from.name === 'instancePulls' || from.name === 'pulls' || from.name === 'instanceQueue' || from.name === 'queue') {
    return { hash: href(from), label: `${'slug' in from ? from.slug : 'all accounts'} · ${from.name.toLowerCase().includes('queue') ? 'Queue' : 'Pull requests'}`, depth: 1 };
  }
  if (router.origin) return { ...router.origin, depth: router.origin.depth + 1 };
}

export function returnToList(): void {
  if (router.origin) history.go(-router.origin.depth);
}

export function navigate(to: Route): void {
  const next = href(to);
  if (location.hash === next) {
    router.route = to;
  } else {
    const origin = originFor(to);
    history.pushState({ kritikaEntry: newEntry(), kritikaOrigin: origin }, '', next);
    router.origin = origin;
    router.entry = entryID();
    router.traversal = false;
    router.route = to;
  }
}

// replace preserves the history entry while synchronizing in-page filters.
export function replace(to: Route): void {
  router.traversal = false;
  if (to.name !== 'pull' && to.name !== 'review') router.origin = undefined;
  router.route = to;
  try {
    history.replaceState({ ...history.state, kritikaOrigin: $state.snapshot(router.origin) }, '', href(to));
  } catch {
    // Safari rate-limits replaceState (SecurityError past ~100 calls/30s).
    // The in-memory route is already updated. The URL syncs on the next call.
  }
}

export function initRouter(): void {
  const changed = () => {
    const existing = typeof history.state?.kritikaEntry === 'string';
    const entry = entryID();
    if (entry === router.entry && href(router.route) === href(parse(location.hash))) return;
    const next = parse(location.hash);
    const origin = existing ? history.state?.kritikaOrigin : originFor(next);
    history.replaceState({ ...history.state, kritikaOrigin: origin }, '');
    router.origin = origin;
    router.entry = entry;
    router.traversal = existing;
    router.route = parse(location.hash);
  };
  window.addEventListener('popstate', changed);
  window.addEventListener('hashchange', changed);
}
