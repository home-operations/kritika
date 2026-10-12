import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';
import type { EventKind, UserSettings } from '../src/lib/types';
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
  held.release();
  await expect.poll(() => moreReads).toBe(2);
  await expect(page.locator('.pull-title')).toHaveText(['Pull 1', 'Pull 2']);
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
    return json(route, ready ? g.diffWithHunk : { diff: '', deltaDiff: '', swept: false });
  });
  await page.goto(`${account}/reviews/rev-1/diff`);
  await expect(page.getByText('No diff recorded for this review.')).toBeVisible();
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
  test(`usage summaries refresh with the chart on ${kind}`, async ({ page }) => {
    let changed = false;
    let usageReads = 0;
    await page.route('**/api/v1/accounts', (route) => json(route, [{ ...g.accountSummary, usage: { ...g.accountSummary.usage, costUsd: changed ? 150 : 1.5, reviewsToday: changed ? 25 : 1, tokens: changed ? 9000 : 1000 } }]));
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
    expect(usageReads).toBeGreaterThan(before);
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
