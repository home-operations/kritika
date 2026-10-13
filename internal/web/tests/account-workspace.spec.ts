import { test, expect } from './fixtures';
import * as g from './golden';
import type { InstancePull } from '../src/lib/types';

test.use({ viewport: { width: 1440, height: 1000 } });

const beta = 'github/beta';
const pulls: InstancePull[] = [
  { ...g.pull, account: g.SLUG, title: 'Fix shutdown ordering', lastReview: { ...g.pull.lastReview!, status: 'failed' } },
  { ...g.pull, account: beta, title: 'Add repository indexing', lastReview: { ...g.pull.lastReview!, status: 'running' } },
];

async function workspace(page: Parameters<typeof g.mockApi>[0]) {
  await page.clock.setFixedTime(new Date('2026-10-12T16:00:00Z'));
  await page.addInitScript(() => {
    window.EventSource = class extends EventTarget {
      constructor() { super(); queueMicrotask(() => this.dispatchEvent(new Event('open'))); }
      close() {}
    } as unknown as typeof EventSource;
  });
  return g.mockApi(page, [
    [/\/api\/v1\/me$/, { ...g.me, accounts: [g.SLUG, beta] }],
    [/\/api\/v1\/accounts$/, [g.accountSummary, { ...g.accountSummary, slug: beta, attention: { failed: 0, capped: 0, p0: 0, paused: 0 } }]],
    [/\/api\/v1\/pulls$/, (url: URL) => g.pageOf(url.searchParams.get('cursor') ? [{ ...pulls[0]!, number: 8, title: 'Harden webhook delivery' }] : pulls, url.searchParams.get('cursor') ? null : 'next')],
    [/\/api\/v1\/queue$/, { ...g.instanceQueue, jobs: [
      { ...g.instanceQueue.jobs[0]!, account: g.SLUG, state: 'running' },
      { ...g.instanceQueue.jobs[0]!, account: beta, id: 1002, state: 'available' },
    ] }],
    [/\/accounts\/github\/alpha\/attention$/, g.accountSummary.attention],
    [/\/accounts\/github\/alpha\/queue$/, [{ ...g.job, state: 'running' }]],
    [/\/accounts\/github\/beta\/pulls$/, g.pageOf([pulls[1]])],
    ...g.defaultApi(),
  ]);
}

for (const path of ['/', '/#/signin', `/#/a/${g.SLUG}/pulls`]) {
  test(`session loading never mounts dashboard or login at ${path}`, async ({ page, mockProviders }, testInfo) => {
    await workspace(page);
    await mockProviders();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/api/v1/me', async (r) => { await gate; await r.fulfill({ status: 401, json: { code: 'unauthenticated', message: 'Sign in' } }); });
    const requests: string[] = [];
    page.on('request', (r) => { if (r.url().includes('/api/v1/')) requests.push(new URL(r.url()).pathname); });
    await page.goto(path);
    await expect(page.getByRole('status')).toHaveText('Loading your workspace…');
    await expect(page.locator('.app, .signin-card')).toHaveCount(0);
    expect(requests.filter((p) => !['/api/v1/me', '/api/v1/meta'].includes(p))).toEqual([]);
    if (path === '/') await page.screenshot({ path: testInfo.outputPath('session-loading.png') });
    release();
    await expect(page.locator('.signin-card')).toBeVisible();
    if (path === '/') await page.screenshot({ path: testInfo.outputPath('sign-in.png') });
    await expect(page.locator('.app')).toHaveCount(0);
  });
}

test('session errors offer retry and keep the requested route', async ({ page }) => {
  await workspace(page);
  let fail = true;
  await page.route('**/api/v1/me', (r) => r.fulfill(fail ? { status: 503, json: { message: 'Temporarily unavailable' } } : { json: g.me }));
  await page.goto(`/#/a/${g.SLUG}/pulls`);
  await expect(page.getByRole('heading', { name: 'Unable to load your session' })).toBeVisible();
  await expect(page.locator('.app, .signin-card')).toHaveCount(0);
  fail = false;
  await page.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page.locator('.pull-row')).toHaveCount(1);
  await expect(page).toHaveURL(new RegExp(`/a/${g.SLUG}/pulls$`));
});

test('a stalled session check times out and can recover on the requested route', async ({ page }, testInfo) => {
  await workspace(page);
  await page.clock.install();
  let calls = 0;
  await page.route('**/api/v1/me', (r) => { if (++calls > 1) return r.fulfill({ json: g.me }); });
  await page.goto(`/#/a/${g.SLUG}/pulls`);
  await expect.poll(() => calls).toBe(1);
  await page.clock.runFor(15_001);
  await expect(page.getByRole('alert')).toContainText('Session request timed out');
  await expect(page.locator('.app, .signin-card')).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath('session-timeout.png') });
  await page.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page.locator('.pull-row')).toHaveCount(1);
  await expect(page).toHaveURL(new RegExp(`/a/${g.SLUG}/pulls$`));
});

for (const status of [403, 503]) {
  test(`explicit sign-in stays available after a ${status} session failure`, async ({ page, mockProviders }) => {
    await workspace(page);
    await mockProviders();
    await page.route('**/api/v1/me', (r) => r.fulfill({ status, contentType: 'text/html', body: '<h1>Unavailable</h1>' }));
    await page.goto('/#/signin');
    await expect(page.locator('.signin-card')).toBeVisible();
    await expect(page.getByRole('link', { name: /GitHub/ })).toBeVisible();
    await expect(page.locator('.app')).toHaveCount(0);
  });
}

test('overview search excludes analytics while recent pulls retain account priority', async ({ page }) => {
  await workspace(page);
  await page.route('**/accounts/github/alpha/pulls?*', (r) => r.fulfill({ json: g.pageOf([{ ...g.pull, title: 'Older current-account pull', updatedAt: '2026-01-01T00:00:00Z' }]) }));
  await page.route('**/accounts/github/beta/pulls?*', (r) => r.fulfill({ json: g.pageOf(Array.from({ length: 20 }, (_, i) => ({ ...g.pull, number: i + 1, title: `Newer other-account pull ${i}`, updatedAt: '2026-10-01T00:00:00Z' }))) }));
  await page.goto(`/#/a/${g.SLUG}`);
  await page.keyboard.press('ControlOrMeta+k');
  const recent = page.locator('.palette-row').filter({ hasText: /account pull/ });
  await expect(recent).toHaveCount(21);
  await expect(recent.first()).toContainText('Older current-account pull');
  await page.getByRole('combobox', { name: 'Go to' }).fill('overview');
  await expect(page.locator('.palette-row')).toHaveCount(3);
  await expect(page.locator('.palette-row').filter({ hasText: 'Analytics' })).toHaveCount(0);
});

test('global pulls keep account identity, pagination, filters and return context', async ({ page }, testInfo) => {
  await workspace(page);
  await page.goto('/#/pulls?state=all');
  await expect(page.locator('.pull-row')).toHaveCount(2);
  await expect(page.getByRole('columnheader', { name: 'Account', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect(page.locator('.pull-row')).toHaveCount(3);
  await testInfo.attach('global-pulls', { body: await page.screenshot({ path: testInfo.outputPath('global-pulls.png') }), contentType: 'image/png' });
  await page.getByRole('link', { name: 'Fix shutdown ordering' }).click();
  await expect(page.locator('.account-button')).toHaveText(g.SLUG);
  await page.reload();
  await page.getByRole('link', { name: 'Back to all accounts · Pull requests' }).click();
  await expect(page).toHaveURL(/#\/pulls\?state=all$/);
  await expect(page.locator('.pull-row')).toHaveCount(2);
  await page.locator('.account-button').click();
  await page.getByRole('navigation', { name: 'Scope' }).getByRole('link', { name: beta, exact: true }).click();
  await expect(page).toHaveURL(/\/a\/github\/beta\/pulls\?state=all$/);
  await page.locator('.account-button').click();
  await page.getByRole('navigation', { name: 'Scope' }).getByRole('link', { name: 'All accounts', exact: true }).click();
  await expect(page).toHaveURL(/#\/pulls\?state=all$/);
});

test('overview and account navigation expose consistent scope', async ({ page }, testInfo) => {
  await workspace(page);
  await page.goto('/#/');
  await expect(page.locator('.account-breakdown tbody tr')).toHaveCount(2);
  await testInfo.attach('all-accounts-overview', { body: await page.screenshot({ path: testInfo.outputPath('all-accounts-overview.png') }), contentType: 'image/png' });
  await page.locator('.desktop-navigation').getByRole('link', { name: 'Queue', exact: true }).click();
  await expect(page.locator('.queue-table tbody tr')).toHaveCount(2);
  await testInfo.attach('global-queue', { body: await page.screenshot({ path: testInfo.outputPath('global-queue.png') }), contentType: 'image/png' });
  await page.goto(`/#/a/${g.SLUG}`);
  await expect(page.getByRole('heading', { name: 'Overview', exact: true })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Account activity' })).toBeVisible();
  await expect(page.locator('.account-activity .stat-value')).toHaveCount(3);
  await expect(page.getByRole('region', { name: 'Repositories', exact: true }).getByRole('link', { name: 'alpha/one', exact: true })).toBeVisible();
  const nav = page.locator('.desktop-navigation');
  for (const link of await nav.getByRole('link').evaluateAll((links) => links.map((l) => l.getAttribute('href')))) expect(link).toContain(`/a/${g.SLUG}`);
  await expect(nav.getByRole('link', { name: 'Overview', exact: true })).toHaveAttribute('aria-current', 'page');
  await testInfo.attach('account-overview', { body: await page.screenshot({ path: testInfo.outputPath('account-overview.png') }), contentType: 'image/png' });
  await nav.getByRole('link', { name: 'Analytics', exact: true }).click();
  await expect(page).toHaveURL(/\/analytics$/);
  await expect(page.getByRole('heading', { name: 'Analytics', exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/#/a/${g.SLUG}`);
  await expect(page.getByRole('region', { name: 'Account activity' })).toBeVisible();
  await expect(page.locator('.account-activity .stat-value')).toHaveCount(3);
  await expect(page.getByRole('region', { name: 'Repositories', exact: true }).getByRole('link', { name: 'alpha/one', exact: true })).toBeVisible();
  await testInfo.attach('account-mobile', { body: await page.screenshot({ path: testInfo.outputPath('account-mobile.png') }), contentType: 'image/png' });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('a delayed queue reserves its overview tile', async ({ page }) => {
  await workspace(page);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/v1/queue', async (r) => { await gate; await r.fulfill({ json: g.instanceQueue }); });
  await page.goto('/#/');
  const tiles = page.getByRole('region', { name: 'Across all accounts' });
  await expect(tiles.locator('.stat')).toHaveCount(3);
  const reviews = tiles.locator('.stat').filter({ hasText: 'Reviews, last 7 days' });
  const before = await reviews.boundingBox();
  release();
  await expect(tiles.getByRole('link', { name: /Running now/ })).toBeVisible();
  expect(await reviews.boundingBox()).toEqual(before);
});

test('returning from a global pull restores the loaded list and scroll', async ({ page }) => {
  await workspace(page);
  const items = Array.from({ length: 90 }, (_, i) => ({ ...pulls[i % 2]!, number: i + 1, title: `Change ${i + 1}` }));
  await page.route('**/api/v1/pulls?*', (r) => {
    const q = new URL(r.request().url()).searchParams;
    const start = Number(q.get('cursor') ?? 0);
    const end = Math.min(start + Number(q.get('limit') ?? 50), items.length);
    return r.fulfill({ json: g.pageOf(items.slice(start, end), end < items.length ? String(end) : null) });
  });
  await page.route('**/pulls/alpha/one/81', (r) => r.fulfill({ json: g.pullDetail }));
  await page.goto('/#/pulls?state=all');
  await page.getByRole('button', { name: 'Load more' }).click();
  const link = page.getByRole('link', { name: 'Change 81', exact: true });
  await link.scrollIntoViewIfNeeded();
  const top = await page.locator('main.page').evaluate((el) => el.scrollTop);
  expect(top).toBeGreaterThan(1000);
  await link.click();
  await page.getByRole('link', { name: 'Back to all accounts · Pull requests' }).click();
  await expect(page.locator('.pull-row')).toHaveCount(90);
  await expect(page).toHaveURL(/#\/pulls\?state=all$/);
  await expect.poll(async () => Math.abs(await page.locator('main.page').evaluate((el) => el.scrollTop) - top)).toBeLessThan(3);
});
