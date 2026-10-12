import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';
import type { AccountDetail, EventKind, UserSettings } from '../src/lib/types';
import * as g from './golden';

const account = `/#/a/${g.SLUG}`;
const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

// Keep the stream open and deliver events explicitly, so reconnect resyncs
// cannot mask a missing subscription or race a deliberately held request.
test.beforeEach(async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.addInitScript(() => {
    window.EventSource = class extends EventTarget {
      private receive = (event: Event) => {
        const { kind, data } = (event as CustomEvent).detail;
        this.dispatchEvent(new MessageEvent(kind, { data: JSON.stringify(data) }));
      };
      constructor() {
        super();
        window.addEventListener('test-live-event', this.receive);
        queueMicrotask(() => this.dispatchEvent(new Event('open')));
      }
      close() { window.removeEventListener('test-live-event', this.receive); }
    } as unknown as typeof EventSource;
  });
});

async function emit(page: Page, kind: EventKind | 'resync', reviewId = 'rev-1'): Promise<void> {
  await page.evaluate((detail) => window.dispatchEvent(new CustomEvent('test-live-event', { detail })), {
    kind, data: { kind, account: g.SLUG, reviewId },
  });
}

function gate() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => (release = resolve));
  return { promise, release };
}

test('failed sign-out keeps the session visible and can be retried', async ({ page, mockProviders }) => {
  let calls = 0;
  await mockProviders();
  await page.route('**/auth/logout', (route) => ++calls === 1
    ? json(route, { code: 'internal', message: 'Server unavailable' }, 500)
    : route.fulfill({ status: 204 }));
  await page.goto('/#/settings');
  await page.locator('.user-button').click();
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Sign out failed');
  await expect(page).toHaveURL(/#\/settings$/);
  await expect(page.locator('.user-button')).toBeVisible();
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page).toHaveURL(/#\/signin$/);
  expect(calls).toBe(2);
});

for (const [path, label] of [['pulls', 'Search pull requests'], ['findings', 'Search findings']] as const) {
  test(`${path} accepts repository filters outside the first suggestion page`, async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    const repos = Array.from({ length: 100 }, (_, i) => ({ ...g.repoPage.items[0]!, id: `repo-${i}`, fullName: `alpha/repo-${i}` }));
    await page.route('**/repos?*', (route) => json(route, g.pageOf(repos, 'next-repos')));
    await page.goto(`${account}/${path}`);
    await page.getByRole('combobox', { name: label }).fill('repo:omega/later');
    await expect.poll(() => seen.some((u) => u.pathname.endsWith(`/${path}`) && u.searchParams.get('repo') === 'omega/later')).toBe(true);
    await expect(page).toHaveURL(/repo=omega%2Flater/);
    await expect(page.locator('.token-note')).toHaveCount(0);
  });
}

for (const text of ['', 'repo:alpha/one ']) {
  test(`Tab leaves search without inserting an unselected suggestion: ${JSON.stringify(text)}`, async ({ page }) => {
    await page.goto(`${account}/pulls`);
    const search = page.getByRole('combobox', { name: 'Search pull requests' });
    await search.fill(text);
    await expect(search).toHaveAttribute('aria-expanded', 'true');
    await page.keyboard.press('Tab');
    await expect(search).not.toBeFocused();
    await expect(search).toHaveValue(text);
  });
}

const pull = (number: number, title = `Pull ${number}`) => ({ ...g.pull, number, title, url: g.pull.url.replace(/\d+$/, String(number)) });

for (const removed of [false, true]) {
  test(`live pagination refreshes loaded pages and follows new cursors: removed=${removed}`, async ({ page }) => {
    let changed = false;
    const cursors: (string | null)[] = [];
    await page.route('**/pulls?*', (route) => {
      const cursor = new URL(route.request().url()).searchParams.get('cursor');
      cursors.push(cursor);
      if (!cursor) return json(route, changed ? g.pageOf([pull(5), pull(1)], 'new') : g.pageOf([pull(1), pull(2)], 'old'));
      return json(route, changed ? g.pageOf([pull(2), removed ? pull(4) : pull(3, 'Updated pull 3')], 'last') : g.pageOf([pull(3), pull(4)], 'last'));
    });
    await page.goto(`${account}/pulls`);
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.locator('.pull-title')).toHaveText(['Pull 1', 'Pull 2', 'Pull 3', 'Pull 4']);
    changed = true;
    await emit(page, 'review');
    await expect(page.locator('.pull-title')).toHaveText(['Pull 5', 'Pull 1', 'Pull 2', removed ? 'Pull 4' : 'Updated pull 3']);
    expect(cursors).toEqual([null, 'old', null, 'new']);
  });
}

test('a live refresh waits for a pending Load more, then refreshes both pages', async ({ page }) => {
  const held = gate();
  let firstReads = 0;
  let moreReads = 0;
  await page.route('**/pulls?*', async (route) => {
    if (!new URL(route.request().url()).searchParams.has('cursor')) {
      firstReads++;
      return json(route, g.pageOf([pull(1)], 'more'));
    }
    if (++moreReads === 1) await held.promise;
    return json(route, g.pageOf([pull(2)]));
  });
  await page.goto(`${account}/pulls`);
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect.poll(() => moreReads).toBe(1);
  await emit(page, 'review');
  await page.waitForTimeout(400); // Let the live debounce expire while Load more is held.
  expect(firstReads).toBe(1);
  await emit(page, 'review');
  await page.waitForTimeout(400);
  held.release();
  await expect.poll(() => moreReads).toBe(2);
  await expect(page.locator('.pull-title')).toHaveText(['Pull 1', 'Pull 2']);
  await page.waitForTimeout(400);
  expect(firstReads).toBe(2);
});

test('events during a slow refresh coalesce while each loaded snapshot still appears', async ({ page }) => {
  const firstRefresh = gate();
  const nextRefresh = gate();
  let reads = 0;
  await page.route('**/pulls?*', async (route) => {
    const read = ++reads;
    if (read === 2) await firstRefresh.promise;
    if (read === 3) await nextRefresh.promise;
    return json(route, g.pageOf([pull(read)]));
  });
  await page.goto(`${account}/pulls`);
  await expect(page.locator('.pull-title')).toHaveText(['Pull 1']);
  await emit(page, 'review');
  await expect.poll(() => reads).toBe(2);
  await emit(page, 'review');
  await page.waitForTimeout(400); // A second event arrives before the held refresh finishes.
  expect(reads).toBe(2);
  firstRefresh.release();
  await expect(page.locator('.pull-title')).toHaveText(['Pull 2']);
  await expect.poll(() => reads).toBe(3);
  nextRefresh.release();
  await expect(page.locator('.pull-title')).toHaveText(['Pull 3']);
});

test('a filter failure hides the old query and Retry loads the selected query', async ({ page }) => {
  let fail = true;
  await page.route('**/pulls?*', (route) => {
    const closed = new URL(route.request().url()).searchParams.get('state') === 'closed';
    if (closed && fail) return json(route, { message: 'Server unavailable' }, 500);
    return json(route, g.pageOf([{ ...pull(closed ? 2 : 1), state: closed ? 'closed' : 'open' }]));
  });
  await page.goto(`${account}/pulls`);
  await expect(page.locator('.pull-title')).toHaveText(['Pull 1']);
  await page.getByRole('radio', { name: 'Closed', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Server unavailable');
  await expect(page.locator('.pull-row')).toHaveCount(0);
  fail = false;
  await page.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page.locator('.pull-title')).toHaveText(['Pull 2']);
});

test('a failed later page leaves the loaded snapshot intact and can be retried', async ({ page }) => {
  let fail = false;
  let refreshed = false;
  await page.route('**/pulls?*', (route) => {
    if (!new URL(route.request().url()).searchParams.has('cursor')) return json(route, g.pageOf([pull(refreshed ? 3 : 1)], 'more'));
    if (fail) return json(route, { message: 'Later page unavailable' }, 500);
    return json(route, g.pageOf([pull(2)]));
  });
  await page.goto(`${account}/pulls`);
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect(page.locator('.pull-title')).toHaveText(['Pull 1', 'Pull 2']);
  fail = true;
  refreshed = true;
  await emit(page, 'review');
  await expect(page.getByRole('alert')).toContainText('Refresh failed');
  await expect(page.locator('.pull-title')).toHaveText(['Pull 1', 'Pull 2']);
  fail = false;
  await page.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page.locator('.pull-title')).toHaveText(['Pull 3', 'Pull 2']);
});

test('a pending diff refreshes on review updates and is cached once available', async ({ page }) => {
  let ready = false;
  let diffReads = 0;
  let reviewReads = 0;
  await page.route('**/reviews/rev-1', (route) => {
    reviewReads++;
    return json(route, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'running' } });
  });
  await page.route('**/reviews/rev-1/diff', (route) => {
    diffReads++;
    return json(route, ready ? g.diffWithHunk : { diff: '', deltaDiff: '', swept: false, pending: true });
  });
  await page.goto(`${account}/reviews/rev-1/diff`);
  await expect(page.getByText('Preparing the diff…')).toBeVisible();
  ready = true;
  await emit(page, 'runner_run');
  await expect(page.locator('.diff-file')).toHaveCount(1);
  expect(diffReads).toBe(2);
  const before = reviewReads;
  await emit(page, 'model_call');
  await expect.poll(() => reviewReads).toBeGreaterThan(before);
  expect(diffReads).toBe(2);
});

for (const kind of ['model_call', 'review', 'resync'] as const) {
  test(`usage refreshes only the affected resources on ${kind}`, async ({ page }) => {
    let changed = false;
    let usageReads = 0;
    let listReads = 0;
    await page.route('**/api/v1/accounts', (route) => { listReads++; return json(route, [{ ...g.accountSummary, usage: { ...g.accountSummary.usage, costUsd: changed ? 150 : 1.5, reviewsToday: changed ? 25 : 1, tokens: changed ? 9000 : 1000 } }]); });
    await page.route('**/api/v1/accounts/' + g.SLUG, (route) => json(route, { ...g.golden<AccountDetail>('account_detail'), usage: { ...g.accountSummary.usage, costUsd: 150, reviewsToday: 25, tokens: 9000 } }));
    await page.route('**/usage?*', (route) => { usageReads++; return json(route, g.usageSeries); });
    await page.goto(`${account}/usage`);
    const month = page.getByRole('region', { name: 'This month' });
    await expect(month).toContainText('$1.50');
    const before = usageReads;
    changed = true;
    await emit(page, kind);
    await expect(month).toContainText('$150.00');
    await expect(month.getByRole('meter', { name: 'Monthly tokens used' })).toHaveAttribute('aria-valuenow', '9000');
    await expect(month.getByRole('meter', { name: 'Reviews today' })).toHaveAttribute('aria-valuenow', '25');
    expect(usageReads).toBe(kind === 'review' ? before : before + 1);
    expect(listReads).toBe(1);
  });
}

for (const statuses of [[204, 204], [500, 204], [204, 500], [500, 500]]) {
  test(`settings writes are serialized and rollback to acknowledged values: ${statuses.join(',')}`, async ({ page }) => {
    let stored: UserSettings = { ...g.NO_SETTINGS };
    const pending: Route[] = [];
    await page.route('**/api/v1/me', (route) => json(route, { ...g.me, settings: stored }));
    await page.route('**/api/v1/me/settings', (route) => { pending.push(route); });
    await page.goto('/#/settings');
    await page.getByRole('radio', { name: 'Dark', exact: true }).click();
    await expect.poll(() => pending.length).toBe(1);
    await page.getByRole('radio', { name: '24-hour', exact: true }).click();
    await expect(page.getByRole('radio', { name: '24-hour', exact: true })).toBeChecked();
    await page.waitForTimeout(100); // The second request must wait for the held first one.
    expect(pending).toHaveLength(1);
    const finish = async (i: number) => {
      const route = pending[i]!;
      if (statuses[i] === 204) { stored = route.request().postDataJSON(); await route.fulfill({ status: 204 }); }
      else await json(route, { message: 'Settings unavailable' }, 500);
    };
    await finish(0);
    await expect.poll(() => pending.length).toBe(2);
    await expect(page.getByRole('status')).not.toContainText('Not saved');
    await expect(page.getByRole('radio', { name: '24-hour', exact: true })).toBeChecked();
    await finish(1);
    const expected = statuses[1] === 204 ? { theme: 'Dark', clock: '24-hour' }
      : { theme: statuses[0] === 204 ? 'Dark' : 'Auto', clock: 'Auto' };
    const assertChoices = async () => {
      await expect(page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: expected.theme, exact: true })).toBeChecked();
      await expect(page.getByRole('radiogroup', { name: 'Clock' }).getByRole('radio', { name: expected.clock, exact: true })).toBeChecked();
    };
    await assertChoices();
    await page.reload();
    await assertChoices();
  });
}

for (const tab of ['timeline', 'raw', 'usage']) {
  test(`the active ${tab} review tab is visible on a phone when deep-linked`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${account}/reviews/rev-1/${tab}`);
    const active = page.getByRole('navigation', { name: 'Review sections' }).locator('[aria-current="page"]');
    await expect(active).toBeInViewport();
    const box = (await active.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  });
}

for (const [path, label] of [['pulls', 'Search pull requests'], ['findings', 'Search findings']] as const) {
  test(path + ' treats unknown repository searches as empty and exposes Clear filters', async ({ page }) => {
    await page.route('**/' + path + '?*', (route) => {
      const name = new URL(route.request().url()).searchParams.get('repo');
      return json(route, g.pageOf(name && name !== 'archived/repo' ? [] : path === 'pulls' ? [g.pull] : [g.accountFinding]));
    });
    await page.route('**/repos?*', (route) => json(route, g.pageOf([])));
    await page.goto(account + '/' + path);
    const search = page.getByRole('combobox', { name: label });
    for (const name of ['home-op', 'typo/repo']) {
      await search.fill('repo:' + name);
      await expect(page.getByRole('button', { name: 'Clear filters' })).toBeVisible();
      await expect(page.getByRole('alert')).toHaveCount(0);
      await page.getByRole('button', { name: 'Clear filters' }).click();
      await expect(search).toHaveValue('');
    }
    await search.fill('repo:archived/repo');
    await expect(page).toHaveURL(/repo=archived%2Frepo/);
    await expect(page.locator('tbody tr')).toHaveCount(1);
  });

  test(path + ' reports unknown static tokens even when repository suggestions fail', async ({ page }) => {
    await page.route('**/repos?*', (route) => json(route, { message: 'Unavailable' }, 500));
    await page.goto(account + '/' + path);
    await page.getByRole('combobox', { name: label }).fill('status:invalid');
    await expect(page.locator('.token-note')).toContainText('status:invalid');
  });
}

for (const key of ['Enter', 'Tab']) {
  test(key + ' tolerates suggestions shrinking during a live update', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    let count = 5;
    await page.route('**/pulls?*', (route) => json(route, g.pageOf(Array.from({ length: count }, (_, i) => ({ ...pull(i + 1), author: 'author-' + i })))));
    await page.goto(account + '/pulls');
    const search = page.getByRole('combobox', { name: 'Search pull requests' });
    await search.fill('author:');
    await expect(page).toHaveURL(/q=author%3A/);
    await expect(page.getByRole('option')).toHaveCount(5);
    for (let i = 0; i < 5; i++) await page.keyboard.press('ArrowDown');
    count = 3;
    await emit(page, 'review');
    await expect(page.getByRole('option')).toHaveCount(3);
    await expect(search).not.toHaveAttribute('aria-activedescendant');
    await page.keyboard.press(key);
    await expect(search).toHaveValue('author:');
    expect(errors).toEqual([]);
  });
}

test('Load more queues behind a busy refresh and uses its fresh cursor', async ({ page }) => {
  const held = gate();
  const requests: (string | null)[] = [];
  let heads = 0;
  await page.route('**/pulls?*', async (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    requests.push(cursor);
    if (!cursor) {
      if (++heads === 2) await held.promise;
      return json(route, g.pageOf([pull(1)], heads === 1 ? 'old' : 'fresh'));
    }
    return json(route, g.pageOf([pull(2)]));
  });
  await page.goto(account + '/pulls');
  await expect(page.locator('.pull-row')).toHaveCount(1);
  await emit(page, 'review');
  await expect.poll(() => heads).toBe(2);
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect(page.getByRole('button', { name: 'Loading…', exact: true })).toBeDisabled();
  await emit(page, 'review');
  await page.waitForTimeout(400);
  held.release();
  await expect(page.locator('.pull-row')).toHaveCount(2);
  expect(requests.slice(0, 3)).toEqual([null, null, 'fresh']);
});

test('explicit reload after a repository action discards a pre-action snapshot', async ({ page }) => {
  const stale = gate();
  const fresh = gate();
  let reads = 0;
  const repo = g.repoPage.items[0]!;
  await page.route('**/repos?*', async (route) => {
    const read = ++reads;
    if (read === 2) await stale.promise;
    if (read === 3) await fresh.promise;
    return json(route, g.pageOf([{ ...repo, enabled: read === 3, index: { ...repo.index, activeCommit: read === 2 ? 'stale00' : 'current' } }]));
  });
  await g.mockWrites(page, [['PUT', /\/turned-on$/, { status: 204 }]]);
  await page.goto(account + '/repos');
  const toggle = page.getByRole('switch');
  await expect(toggle).not.toBeChecked();
  await emit(page, 'index_run');
  await expect.poll(() => reads).toBe(2);
  await toggle.click();
  await expect(page.getByRole('status')).toContainText('turned on');
  stale.release();
  await expect.poll(() => reads).toBe(3);
  await expect(page.locator('table')).not.toContainText('stale00');
  fresh.release();
  await expect(toggle).toBeChecked();
});

test('deep lists refresh in bounded larger requests and shrink their loaded window', async ({ page }) => {
  let count = 600;
  const requests: { limit: number; offset: number }[] = [];
  await page.route('**/pulls?*', (route) => {
    const params = new URL(route.request().url()).searchParams;
    const limit = Number(params.get('limit'));
    const offset = Number(params.get('cursor') ?? 0);
    requests.push({ limit, offset });
    const end = Math.min(count, offset + limit);
    return json(route, g.pageOf(Array.from({ length: Math.max(0, end - offset) }, (_, i) => pull(offset + i + 1)), end < count ? String(end) : null));
  });
  await page.goto(account + '/pulls');
  await expect(page.locator('.pull-row')).toHaveCount(50);
  for (let n = 100; n <= 500; n += 50) {
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.locator('.pull-row')).toHaveCount(n);
  }
  requests.length = 0;
  await emit(page, 'review');
  await expect.poll(() => requests.length).toBe(3);
  expect(requests).toEqual([{ limit: 200, offset: 0 }, { limit: 200, offset: 200 }, { limit: 100, offset: 400 }]);
  count = 120;
  await emit(page, 'review');
  await expect(page.locator('.pull-row')).toHaveCount(120);
  requests.length = 0;
  count = 600;
  await emit(page, 'review');
  await expect.poll(() => requests.length).toBe(1);
  await expect(page.getByRole('button', { name: 'Load more' })).toBeVisible();
  expect(requests).toEqual([{ limit: 120, offset: 0 }]);
  await expect(page.locator('.pull-row')).toHaveCount(120);
});

test('a timed out settings write releases the queue without reporting a superseded failure', async ({ page }) => {
  await page.clock.install();
  let calls = 0;
  await page.route('**/api/v1/me/settings', async (route) => {
    if (++calls === 1) return; // Hold the first response until the client aborts it.
    return route.fulfill({ status: 204 });
  });
  await page.goto('/#/settings');
  await page.getByRole('radio', { name: 'Dark', exact: true }).click();
  await expect.poll(() => calls).toBe(1);
  await page.getByRole('radio', { name: '24-hour', exact: true }).click();
  await page.clock.runFor(15_001);
  await expect.poll(() => calls).toBe(2);
  await expect(page.getByRole('radio', { name: '24-hour', exact: true })).toBeChecked();
  await expect(page.getByRole('status')).not.toContainText('Not saved');
});

test('a final settings timeout reports the failure and restores acknowledged choices', async ({ page }) => {
  await page.clock.install();
  let held = false;
  await page.route('**/api/v1/me/settings', () => { held = true; });
  await page.goto('/#/settings');
  await page.getByRole('radio', { name: 'Dark', exact: true }).click();
  await expect.poll(() => held).toBe(true);
  await page.clock.runFor(15_001);
  await expect(page.getByRole('status')).toContainText('Settings request timed out');
  await expect(page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: 'Auto', exact: true })).toBeChecked();
});

test('a landscape review reveals its tab horizontally without scrolling past its title', async ({ page }) => {
  await page.setViewportSize({ width: 640, height: 320 });
  await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, error: 'A long failure detail. '.repeat(60) } }], ...g.defaultApi()]);
  await page.goto(account + '/reviews/rev-1/usage');
  await expect(page.locator('main h1')).toBeInViewport();
  expect(await page.locator('main').evaluate((el) => el.scrollTop)).toBe(0);
  const active = page.getByRole('navigation', { name: 'Review sections' }).locator('[aria-current="page"]');
  const box = (await active.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(640);
});

for (const swept of [false, true]) {
  test('a recorded empty diff is cached: swept=' + swept, async ({ page }) => {
    let reads = 0;
    let reviews = 0;
    await page.route('**/reviews/rev-1', (route) => { reviews++; return json(route, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'running' } }); });
    await page.route('**/reviews/rev-1/diff', (route) => { reads++; return json(route, { diff: '', deltaDiff: '', swept }); });
    await page.goto(account + '/reviews/rev-1/diff');
    await expect(page.getByText(swept ? 'The diff was not kept past the retention window.' : 'No diff recorded for this review.')).toBeVisible();
    const before = reviews;
    await emit(page, 'model_call');
    await expect.poll(() => reviews).toBeGreaterThan(before);
    expect(reads).toBe(1);
  });
}

test('sign-out errors use the existing actionable hint without duplicate punctuation', async ({ page }) => {
  await page.route('**/auth/logout', (route) => json(route, { code: 'csrf', message: '' }, 403));
  await page.goto('/#/settings');
  await page.locator('.user-button').click();
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page.locator('.toast span')).toHaveText('Sign out failed: The request was refused as not coming from this page; reload and try again.');
});
