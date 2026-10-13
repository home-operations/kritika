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
    kind, data: { kind, account: g.SLUG, id: reviewId, reviewId: kind === 'review' ? null : reviewId },
  });
}

function gate() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => (release = resolve));
  return { promise, release };
}

test('retryable jobs need attention even without recent-failure or overdue flags', async ({ page }) => {
  await page.route('**/accounts/github/alpha/queue', (route) => json(route, [{ ...g.job, state: 'retryable', recentFailure: false, heartbeatOverdue: false }]));
  await page.goto(account);
  await expect(page.getByRole('link', { name: /Inspect failed, retrying or overdue work/ })).toBeVisible();
  await expect(page.getByText('All clear. No review alerts or queue issues.')).toHaveCount(0);
});

for (const failed of ['queue', 'attention', 'account']) {
  test(`account overview survives an independent ${failed} failure and retries it`, async ({ page }, testInfo) => {
    const detail = g.golden<AccountDetail>('account_detail');
    let fail = true;
    await page.route(`**/accounts/github/alpha${failed === 'account' ? '' : '/' + failed}`, async (route) => {
      if (fail) return json(route, { message: `${failed} unavailable` }, 503);
      return route.fallback();
    });
    await page.goto(account);
    await expect(page.getByRole('alert')).toContainText(`${failed} unavailable`);
    await expect(page.getByRole('region', { name: 'Repositories', exact: true }).getByRole('link', { name: 'alpha/one', exact: true })).toBeVisible();
    const tiles = page.getByRole('region', { name: 'Account activity' });
    await expect(tiles.locator('.tile')).toHaveCount(4);
    if (failed !== 'account') {
      await expect(page.getByRole('region', { name: 'Connection', exact: true })).toContainText(detail.connection.name);
      await expect(tiles.locator('.tile').filter({ hasText: 'Reviews today' }).locator('.tile-value')).toHaveText(String(detail.usage.reviewsToday));
    }
    if (failed !== 'queue') await expect(tiles.locator('.tile').filter({ hasText: 'Running now' }).locator('.tile-value')).toHaveText(/\d+/);
    await expect(page.getByText('All clear. No review alerts or queue issues.')).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath(`account-${failed}-unavailable.png`) });
    fail = false;
    await page.getByRole('alert').getByRole('button', { name: /Retry/ }).click();
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expect(tiles.locator('.tile-value')).toHaveCount(4);
  });
}

test('account overview polls only jobs and refreshes only data affected by live events', async ({ page }) => {
  await page.clock.install();
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(account);
  await expect(page.getByRole('region', { name: 'Repositories', exact: true }).getByRole('link', { name: 'alpha/one', exact: true })).toBeVisible();
  await expect(page.locator('.account-activity .tile-value')).toHaveCount(4);
  const calls = () => seen.filter((u) => u.pathname.startsWith('/api/v1/accounts/')).map((u) => u.pathname.replace('/api/v1/accounts/github/alpha', '')).sort();
  seen.length = 0;
  await page.clock.runFor(15_001);
  await expect.poll(calls).toEqual(['/queue']);
  seen.length = 0;
  await emit(page, 'runner_run');
  await page.clock.runFor(301);
  await expect.poll(calls).toEqual(['/queue']);
  seen.length = 0;
  await emit(page, 'index_run');
  await page.clock.runFor(301);
  await expect.poll(calls).toEqual(['/queue', '/repos']);
  seen.length = 0;
  await emit(page, 'review');
  await page.clock.runFor(301);
  await expect.poll(calls).toEqual(['', '/attention', '/queue', '/repos']);
  seen.length = 0;
  await emit(page, 'followup');
  await page.clock.runFor(301);
  await expect.poll(calls).toEqual(['', '/queue']);
  seen.length = 0;
  await emit(page, 'resync');
  await page.clock.runFor(301);
  await expect.poll(calls).toEqual(['', '/attention', '/queue', '/repos']);
});

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
    await search.press('ArrowUp');
    await expect(search).toHaveAttribute('aria-activedescendant', (await page.getByRole('option').last().getAttribute('id'))!);
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

test('hidden review tabs defer live refetches until they become visible', async ({ page }) => {
  let transcriptCalls = 0;
  let rawCalls = 0;
  let reviewCalls = 0;
  await page.route('**/reviews/rev-1', (route) => { reviewCalls++; return json(route, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'running' } }); });
  await page.route('**/reviews/rev-1/transcript', (route) => { transcriptCalls++; return json(route, g.transcript); });
  await page.route('**/reviews/rev-1/raw', (route) => { rawCalls++; return json(route, g.reviewRaw); });
  await page.goto(`${account}/reviews/rev-1/conversation`);
  await expect(page.locator('.conversation-top')).toBeVisible();
  await page.getByRole('link', { name: 'Raw', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Submitted result' })).toBeVisible();
  await page.getByRole('navigation', { name: 'Review sections' }).getByRole('link', { name: 'Overview' }).click();
  await emit(page, 'model_call');
  await expect.poll(() => reviewCalls).toBe(2);
  expect(transcriptCalls).toBe(1);
  expect(rawCalls).toBe(1);
  await page.getByRole('link', { name: 'Run details', exact: true }).click();
  await page.getByRole('link', { name: 'Conversation', exact: true }).click();
  await expect.poll(() => transcriptCalls).toBe(2);
  expect(rawCalls).toBe(1);
  await page.getByRole('link', { name: 'Raw', exact: true }).click();
  await expect.poll(() => rawCalls).toBe(2);
});

test('reconnect status stays inside the mobile header without obscuring content', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${account}/pulls`);
  await expect(page.locator('.page-head h1')).toBeVisible();
  await page.evaluate(() => {
    window.EventSource = class extends EventTarget { close() {} } as unknown as typeof EventSource;
    window.dispatchEvent(new CustomEvent('test-live-event', { detail: { kind: 'error', data: {} } }));
  });
  await expect(page.locator('.live-down')).toBeVisible();
  const header = (await page.locator('.topbar').boundingBox())!;
  const notice = (await page.locator('.live-down').boundingBox())!;
  const main = (await page.locator('main').boundingBox())!;
  expect(notice.y + notice.height).toBeLessThanOrEqual(header.y + header.height);
  expect(notice.y + notice.height).toBeLessThanOrEqual(main.y);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
});

test('the overview summarizes many accounts without fetching individual pull lists', async ({ page }) => {
  const accounts = Array.from({ length: 25 }, (_, i) => ({ ...g.accountSummary, slug: `github/account-${i}`, attention: { failed: 1, capped: 1, p0: 1, paused: 1 } }));
  const calls: string[] = [];
  await page.route('**/api/v1/accounts', (route) => json(route, accounts));
  await page.route('**/pulls?*', (route) => { calls.push(route.request().url()); return json(route, g.pageOf([g.pull])); });
  await page.goto('/');
  const attention = page.getByRole('region', { name: 'Needs attention', exact: true });
  await expect(attention.getByRole('link')).toHaveCount(75);
  await attention.getByRole('link').last().scrollIntoViewIfNeeded();
  expect(calls).toEqual([]);
  accounts[0]!.attention.failed = 2;
  const refreshed = page.waitForResponse((res) => new URL(res.url()).pathname === '/api/v1/accounts');
  await page.evaluate(() => window.dispatchEvent(new CustomEvent('test-live-event', { detail: { kind: 'review', data: { kind: 'review', account: 'github/account-0', reviewId: 'rev-1' } } })));
  await refreshed;
  await expect(attention.getByRole('link').first()).toContainText('2 failed');
  expect(calls).toEqual([]);
});


test('the PR workspace offers newer runs without replacing the displayed review', async ({ page }) => {
  const second = { ...g.reviewDetail.review, id: 'rev-2', headSha: '2222222' };
  const third = { ...second, id: 'rev-3', headSha: '3333333' };
  let reviews = [g.reviewDetail.review];
  await page.route('**/pulls/alpha/one/7', (route) => json(route, { ...g.pullDetail, reviews }));
  await page.route('**/reviews/rev-2', (route) => json(route, { ...g.reviewDetail, review: second, summary: { ...g.reviewDetail.summary!, take: 'Second review conclusion' } }));
  await page.goto(`${account}/pulls/alpha/one/7`);
  await expect(page.getByRole('region', { name: 'Take', exact: true })).toContainText(g.reviewDetail.summary!.take);
  reviews = [second, g.reviewDetail.review];
  await emit(page, 'review', second.id);
  await expect(page.locator('.newer-notice')).toContainText('newer review is available');
  await expect(page.getByRole('region', { name: 'Take', exact: true })).toContainText(g.reviewDetail.summary!.take);
  await page.getByRole('link', { name: 'Open its latest review' }).click();
  await expect(page.getByRole('region', { name: 'Take', exact: true })).toContainText('Second review conclusion');
  await expect(page.getByLabel('Review run')).toHaveValue('');
  await page.getByLabel('Review run').selectOption('rev-1');
  await expect(page.getByRole('region', { name: 'Take', exact: true })).toContainText(g.reviewDetail.summary!.take);
  reviews = [third, second, g.reviewDetail.review];
  await emit(page, 'review', third.id);
  await expect(page.getByLabel('Review run').locator('option[value="rev-3"]')).toHaveCount(1);
  await expect(page.getByLabel('Review run')).toHaveValue('rev-1');
  await expect(page.getByRole('region', { name: 'Take', exact: true })).toContainText(g.reviewDetail.summary!.take);
  await expect(page.locator('.newer-notice')).toContainText('Historical review');
});


test('new runs preserve an open diff and review pages ignore runner traffic for pull metadata', async ({ page }) => {
  let pullReads = 0;
  let reviewReads = 0;
  let reviews = [g.reviewDetail.review];
  await page.route('**/pulls/alpha/one/7', (route) => {
    pullReads++;
    return json(route, { ...g.pullDetail, reviews, job: null });
  });
  await page.route('**/reviews/rev-1', (route) => {
    reviewReads++;
    return json(route, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'running' } });
  });
  await page.goto(`${account}/pulls/alpha/one/7?tab=diff`);
  await page.getByRole('searchbox', { name: 'Search files' }).fill('a.go');
  await page.getByRole('button', { name: 'Hide navigator' }).click();
  const before = pullReads;
  await emit(page, 'runner_run', 'another-review');
  await emit(page, 'followup', 'another-review');
  await emit(page, 'model_call', 'another-review');
  await emit(page, 'runner_run');
  await expect.poll(() => reviewReads).toBe(2);
  expect(pullReads).toBe(before);
  await page.getByRole('link', { name: 'Activity', exact: true }).click();
  await expect.poll(() => pullReads).toBe(before + 1);
  await page.getByRole('link', { name: 'Findings & code', exact: true }).click();
  reviews = [{ ...g.reviewDetail.review, id: 'rev-2' }, ...reviews];
  await emit(page, 'review', 'rev-2');
  await expect(page.locator('.newer-notice')).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Findings and files' })).toBeHidden();
  await page.getByRole('button', { name: 'Show navigator' }).click();
  await expect(page.getByRole('searchbox', { name: 'Search files' })).toHaveValue('a.go');
  await page.getByRole('link', { name: 'Activity', exact: true }).click();
  const activityReads = pullReads;
  await emit(page, 'followup', 'another-review');
  await expect.poll(() => pullReads).toBe(activityReads + 1);
});

test('a live refresh supersedes an unfinished initial queue request', async ({ page }) => {
  const held = gate();
  let reads = 0;
  await page.route('**/accounts/github/alpha/queue', async (route) => {
    if (++reads === 1) {
      await held.promise;
      return json(route, [g.job]);
    }
    return json(route, []);
  });
  await page.goto(`${account}/queue`);
  await expect.poll(() => reads).toBe(1);
  await emit(page, 'runner_run');
  await expect.poll(() => reads).toBe(2);
  await expect(page.locator('[data-loading="true"]')).toHaveCount(0);
  held.release();
  await expect(page.locator('table.data tbody tr')).toHaveCount(0);
});

test('review polling keeps its interval across an unchanged live response', async ({ page }) => {
  await page.clock.install();
  let reads = 0;
  await page.route('**/pulls/alpha/one/7', (route) => {
    reads++;
    return json(route, { ...g.pullDetail, job: g.job });
  });
  await page.goto(`${account}/pulls/alpha/one/7`);
  await expect.poll(() => reads).toBe(1);
  await expect(page.locator('main h1')).toContainText(g.pullDetail.pull.title);
  await page.clock.runFor(10_000);
  await emit(page, 'review');
  await page.clock.runFor(301);
  await expect.poll(() => reads).toBe(2);
  await page.clock.runFor(5_000);
  await expect.poll(() => reads).toBe(3);
});

test('palette data is invalidated on a page without live subscribers', async ({ page }) => {
  let changed = false;
  let reads = 0;
  await page.route('**/pulls?state=all&limit=20', (route) => {
    reads++;
    return json(route, g.pageOf([{ ...g.pull, title: changed ? 'Updated palette pull' : 'Original palette pull' }]));
  });
  await page.goto(`${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  await page.keyboard.press('Control+k');
  await expect(page.locator('.palette-row').filter({ hasText: 'Original palette pull' }).first()).toBeVisible();
  await page.keyboard.press('Escape');
  const before = reads;
  changed = true;
  await emit(page, 'review');
  await page.keyboard.press('Control+k');
  await expect(page.locator('.palette-row').filter({ hasText: 'Updated palette pull' }).first()).toBeVisible();
  expect(reads).toBeGreaterThan(before);
});

for (const change of ['event', 'write'] as const) {
  test(`an unrelated account ${change} leaves cached rules fresh`, async ({ page }) => {
    let reads = 0;
    await page.route('**/accounts/github/alpha/rules', (route) => {
      reads++;
      return json(route, [g.rule]);
    });
    await page.goto(`${account}/rules`);
    await expect(page.locator('main')).toContainText(g.rule.text);
    await page.route('**/accounts/github/beta/queue', (route) => json(route, [{ ...g.job, cancelableReviewId: 'rev-1' }]));
    await page.evaluate(() => { location.hash = '#/a/github/beta/queue'; });
    await expect(page.locator('main h1')).toContainText('Queue');
    if (change === 'event') {
      await page.evaluate(() => window.dispatchEvent(new CustomEvent('test-live-event', { detail: {
        kind: 'model_call', data: { kind: 'model_call', account: 'github/beta', id: 'call-1', reviewId: 'rev-2' },
      } })));
    } else {
      const sent = await g.mockWrites(page, [['POST', /\/accounts\/github\/beta\/reviews\/rev-1\/cancel$/, { status: 202, body: g.accepted }]]);
      await page.locator('.queue-table tbody tr').first().getByRole('button', { name: 'Cancel review' }).click();
      await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel review' }).click();
      await expect.poll(() => sent.length).toBe(1);
      await expect(page.getByRole('status')).toContainText('Cancel requested');
    }
    await page.evaluate((hash) => { location.hash = hash; }, `${account.slice(1)}/rules`);
    await expect(page.locator('main')).toContainText(g.rule.text);
    await expect(page.locator('[data-loading="true"]')).toHaveCount(0);
    expect(reads).toBe(1);
  });
}

test('leaving a list aborts its pending Load more request', async ({ page }) => {
  const held = gate();
  let moreReads = 0;
  let cancelled = false;
  page.on('requestfailed', (request) => {
    if (new URL(request.url()).searchParams.has('cursor')) cancelled = true;
  });
  await page.route('**/pulls?*', async (route) => {
    if (!new URL(route.request().url()).searchParams.has('cursor')) return json(route, g.pageOf([pull(1)], 'more'));
    moreReads++;
    await held.promise;
    return json(route, g.pageOf([pull(2)]));
  });
  await page.goto(`${account}/pulls`);
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect.poll(() => moreReads).toBe(1);
  await page.evaluate((hash) => { location.hash = hash; }, `${account.slice(1)}/rules`);
  await expect.poll(() => cancelled).toBe(true);
  held.release();
  await expect(page.locator('main')).toContainText(g.rule.text);
});

test('offline reads report an error and allow retry without waiting for an online event', async ({ page }) => {
  await page.goto(`${account}/queue`);
  await expect(page.locator('.queue-table')).toBeVisible();
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));
  await page.route('**/rules', (route) => route.abort('internetdisconnected'));
  await page.evaluate((hash) => { location.hash = hash; }, `${account.slice(1)}/rules`);
  await expect(page.getByRole('alert')).toContainText('Failed to fetch');
  await page.unroute('**/rules');
  await page.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page.locator('main')).toContainText(g.rule.text);
});

test('palette logs each failed fetch only once across other query updates', async ({ page }) => {
  const held = gate();
  const errors: string[] = [];
  page.on('console', (message) => {
    if (message.text().startsWith('palette recent pulls:')) errors.push(message.text());
  });
  await page.route('**/api/v1/me', (route) => json(route, { ...g.me, accounts: [g.SLUG, 'github/beta'] }));
  await page.route('**/accounts/github/alpha/pulls?state=all&limit=20', (route) => json(route, { message: 'Palette unavailable' }, 503));
  await page.route('**/accounts/github/beta/pulls?state=all&limit=20', async (route) => {
    await held.promise;
    return json(route, g.pageOf([g.pull]));
  });
  await page.goto(`${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  await page.keyboard.press('Control+k');
  await expect.poll(() => errors.length).toBe(1);
  held.release();
  await expect(page.locator('.palette-row').filter({ hasText: g.pull.title })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('.palette-row')).toHaveCount(0);
  expect(errors).toHaveLength(1);
});

for (const [tab, endpoint, reviewId] of [['diff', 'diff', 'rev-1'], ['diff', 'diff', 'rev-other'], ['raw', 'raw', 'rev-other'], ['conversation', 'transcript', 'rev-other']]) {
  test(`${reviewId === 'rev-1' ? 'same-review' : 'unrelated review'} events preserve the cached ${tab} payload`, async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    const reads = () => seen.filter((url) => url.pathname.endsWith(`/reviews/rev-1/${endpoint}`)).length;
    await page.goto(`${account}/reviews/rev-1/${tab}`);
    await expect.poll(reads).toBe(1);
    await expect(page.locator('[data-loading="true"]')).toHaveCount(0);
    await page.evaluate((hash) => { location.hash = hash; }, `${account.slice(1)}/rules`);
    await expect(page.locator('main')).toContainText(g.rule.text);
    await emit(page, 'review', reviewId);
    await page.evaluate((hash) => { location.hash = hash; }, `${account.slice(1)}/reviews/rev-1/${tab}`);
    await expect(page.locator('main h1')).not.toHaveText('Rules');
    await expect(page.locator('[data-loading="true"]')).toHaveCount(0);
    expect(reads()).toBe(1);
  });
}
