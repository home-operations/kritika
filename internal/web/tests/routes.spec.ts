// Pure round-trip and malformed-input coverage for parse()/href() in
// routes.ts. These are plain functions with no Svelte runes, so -- unlike
// router.spec.ts, which exercises the $state-based router.svelte.ts through
// a real page -- this file calls them directly and needs no browser.
import { test, expect } from '@playwright/test';
import { href, parse, type Route } from '../src/lib/routes';
import { INSTANCE_TABS, pageIn, pageOfInstance, scopeOf, sectionOf, SUB_TABS } from '../src/lib/sections';

const ROUTES: Route[] = [
  { name: 'overview' },
  { name: 'signin' },
  { name: 'console' },
  { name: 'instanceQueue' },
  { name: 'preferences' },
  { name: 'account', slug: 'github/acme' },
  { name: 'repos', slug: 'github/acme' },
  { name: 'repo', slug: 'github/acme', owner: 'kritika', repo: 'kritika' },
  { name: 'pulls', slug: 'github/acme' },
  { name: 'pulls', slug: 'github/acme', filter: { state: 'all', outcome: 'failed', is: 'paused', repo: 'kritika/kritika', author: 'ada', q: 'a b&c#d' } },
  { name: 'pull', slug: 'github/acme', owner: 'kritika', repo: 'kritika', number: 42 },
  { name: 'review', slug: 'github/acme', id: 'r1' },
  { name: 'review', slug: 'github/acme', id: 'r1', tab: 'diff' },
  { name: 'review', slug: 'github/acme', id: 'r1', finding: 'f 1/2' },
  { name: 'findings', slug: 'github/acme' },
  { name: 'rules', slug: 'github/acme' },
  { name: 'findings', slug: 'github/acme', filter: { severity: 'p0', status: 'addressed', repo: 'kritika/kritika', q: 'nil deref' } },
  { name: 'queue', slug: 'github/acme' },
  { name: 'usage', slug: 'github/acme' },
  { name: 'followups', slug: 'github/acme' },
  { name: 'admin', slug: 'github/acme' },
  { name: 'admin', slug: 'github/acme', section: 'tokens' },
  // segments containing characters that must round-trip through
  // encodeURIComponent/decodeURIComponent (slashes, spaces, '#').
  { name: 'account', slug: 'github/b c#d' },
  { name: 'account', slug: 'github/a/b' },
  { name: 'repo', slug: 'github/acme', owner: 'weird/owner', repo: 're po' },
];

test.describe('routes: parse(href(r)) === r', () => {
  for (const route of ROUTES) {
    test(JSON.stringify(route), () => {
      expect(parse(href(route))).toEqual(route);
    });
  }
});

const MALFORMED: [string, Route][] = [
  ['', { name: 'overview' }],
  ['#/', { name: 'overview' }],
  ['#/nonsense', { name: 'overview' }],
  ['#/signin/extra', { name: 'overview' }],
  ['#/admin/extra', { name: 'overview' }],
  ['#/a', { name: 'overview' }],
  ['#/a/', { name: 'overview' }],
  ['#/a//repos', { name: 'overview' }],
  ['#/a//acme', { name: 'overview' }],
  // A slug is a forge and a name; either alone is no account.
  ['#/a/github', { name: 'overview' }],
  ['#/a/github/', { name: 'overview' }],
  ['#/a/github//repos', { name: 'overview' }],
  // A single trailing slash is tolerated and parses like its absence, even
  // on a bare account slug -- this is no longer "malformed" so much as an
  // accepted alternate spelling.
  ['#/a/github/acme/', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/repos/only-owner', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/repos/o/r/extra', { name: 'account', slug: 'github/acme' }],
  // Same case as above, but with a tolerated trailing slash: still falls
  // back to the account overview, not the global one.
  ['#/a/github/acme/repos/o/r/extra/', { name: 'account', slug: 'github/acme' }],
  // A double slash after the slug is downstream of it, so it falls back to
  // that account's overview rather than the global one.
  ['#/a/github/acme//repos', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/pulls/o/r/abc', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/pulls/o/r/-5', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/pulls/o/r/3.5', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/pulls/o/r/1e2', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/queue/extra', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/admin/section/extra', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/reviews/r1/bogus', { name: 'review', slug: 'github/acme', id: 'r1' }],
  ['#/a/github/acme/reviews/r1/diff/extra', { name: 'account', slug: 'github/acme' }],
  ['#/a/github/acme/bogus-section', { name: 'account', slug: 'github/acme' }],
  // A query string is ignored.
  ['#/a/github/acme/repos/o/r?connection=x', { name: 'repo', slug: 'github/acme', owner: 'o', repo: 'r' }],
  ['#/a/github/acme/pulls/o/r/7?other=1', { name: 'pull', slug: 'github/acme', owner: 'o', repo: 'r', number: 7 }],
  // The pull list keeps only the filters it knows, and none that are a default.
  ['#/a/github/acme/pulls?state=open&outcome=bogus&repo=&other=1', { name: 'pulls', slug: 'github/acme' }],
  ['#/a/github/acme/pulls/?state=closed&outcome=bogus', { name: 'pulls', slug: 'github/acme', filter: { state: 'closed' } }],
];

test.describe('routes: parse() on unknown/malformed hashes', () => {
  for (const [hash, expected] of MALFORMED) {
    test(`parse(${JSON.stringify(hash)})`, () => {
      expect(parse(hash)).toEqual(expected);
    });
  }
});

test('every account page belongs to one section, each sub-tab to its own, and the instance\'s pages to none', () => {
  for (const r of ROUTES) {
    expect(sectionOf(r), r.name).toEqual(scopeOf(r) ? expect.any(String) : undefined);
  }
  for (const t of INSTANCE_TABS) expect(scopeOf(t.route), t.label).toBeUndefined();
  for (const [section, tabs] of Object.entries(SUB_TABS)) {
    for (const t of tabs) expect(sectionOf(t.route('github/acme')), t.label).toBe(section);
  }
});

test('switching account keeps the page, and a record gives way to its list', () => {
  const a = 'github/acme';
  const b = 'github/globex';
  const cases: [Route, Route][] = [
    [{ name: 'account', slug: a }, { name: 'account', slug: b }],
    [{ name: 'queue', slug: a }, { name: 'queue', slug: b }],
    [{ name: 'findings', slug: a, filter: { repo: 'acme/one' } }, { name: 'findings', slug: b }],
    [{ name: 'pulls', slug: a, filter: { state: 'all' } }, { name: 'pulls', slug: b }],
    [{ name: 'pull', slug: a, owner: 'acme', repo: 'one', number: 7 }, { name: 'pulls', slug: b }],
    [{ name: 'review', slug: a, id: 'r1', tab: 'diff' }, { name: 'pulls', slug: b }],
    [{ name: 'repo', slug: a, owner: 'acme', repo: 'one' }, { name: 'repos', slug: b }],
    [{ name: 'admin', slug: a, section: 'audit' }, { name: 'admin', slug: b, section: 'audit' }],
    [{ name: 'instanceQueue' }, { name: 'queue', slug: b }],
    [{ name: 'overview' }, { name: 'account', slug: b }],
    [{ name: 'console' }, { name: 'account', slug: b }],
  ];
  for (const [from, to] of cases) expect(pageIn(from, b), JSON.stringify(from)).toEqual(to);
});

test("switching to the instance lands on its queue from an account's, and its overview otherwise", () => {
  const a = 'github/acme';
  expect(pageOfInstance({ name: 'queue', slug: a }, true)).toEqual({ name: 'instanceQueue' });
  expect(pageOfInstance({ name: 'queue', slug: a }, false)).toEqual({ name: 'overview' });
  expect(pageOfInstance({ name: 'pulls', slug: a }, true)).toEqual({ name: 'overview' });
  expect(pageOfInstance({ name: 'console' }, true)).toEqual({ name: 'console' });
});
