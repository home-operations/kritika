import { test, expect } from './fixtures';
import * as g from './golden';
import type { Job, Analytics } from '../src/lib/types';
import { href, parse, usageFilter } from '../src/lib/routes';
import { overdue } from '../src/lib/queue';

const account = `/#/a/${g.SLUG}`;
const now = new Date('2026-10-12T16:00:00Z').getTime();
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const jobs: Job[] = [
  { ...g.job, id: 101, state: 'running', attemptedAt: ago(10), heartbeatAt: ago(0.2), cancelableReviewId: 'rev-1', lastError: '', cause: '' },
  { ...g.job, id: 102, state: 'running', attemptedAt: ago(12), heartbeatAt: ago(5), heartbeatOverdue: true, cancelableReviewId: 'rev-2', lastError: '', cause: '' },
  { ...g.job, id: 103, state: 'retryable', scheduledAt: ago(-2), attemptedAt: ago(3) },
  { ...g.job, id: 104, state: 'discarded', recentFailure: true, finalizedAt: ago(7), lastError: 'Runner exited before publishing its result', cause: '' },
  { ...g.job, id: 105, state: 'available', args: { ...g.job.args, number: 8 }, attempt: 0, attemptedAt: null, lastError: '', cause: '' },
];

const analytics: Analytics = {
  ...g.analytics,
  current: { ...g.analytics.current, reviews: 80, failed: 4, tokens: 8_200_000, costUsd: 42.18 },
  previous: { ...g.analytics.previous, reviews: 60, failed: 8, tokens: 6_900_000, costUsd: 36.4 },
  series: Array.from({ length: 14 }, (_, i) => ({ key: new Date(now - (13 - i) * 86_400_000).toISOString().slice(0, 10), reviews: 3 + i % 5, failed: i % 4 === 0 ? 1 : 0, tokens: 180_000 + i * 15_000, costUsd: 1.2 + i * 0.25, medianReviewMs: 60_000 + i * 2000, findings: { p0: i % 3, p1: 1, p2: 2 } })),
};

async function mockOperations(page: Parameters<typeof g.mockApi>[0]) {
  await page.clock.setFixedTime(now);
  return g.mockApi(page, [
    [/\/queue$/, (url: URL) => url.pathname === '/api/v1/queue' ? { ...g.instanceQueue, jobs: jobs.map((j) => ({ ...j, account: g.SLUG })) } : jobs],
    [/\/analytics$/, analytics],
    [/\/api\/v1\/accounts$/, [{ ...g.accountSummary, usage: { ...g.accountSummary.usage, costUsd: 42.18, tokens: 920_000, reviewsToday: 46 }, lastWebhookAt: null }]],
    [/\/usage$/, (url: URL) => {
      const group = url.searchParams.get('group') ?? 'day';
      const keys = group === 'repo' ? ['alpha/one', 'alpha/platform', ''] : group === 'model' ? ['acme/large', 'acme/fast'] : group === 'review' ? ['rev-1', 'rev-2', ''] : ['2026-10-10', '2026-10-11', '2026-10-12'];
      const rows = keys.map((key, i) => ({ ...g.usageSeries.rows[0]!, key, costUsd: [21.4, 8.2, 0.12][i]!, calls: 25 - i * 5, inputTokens: 620_000 - i * 50_000, outputTokens: 60_000 - i * 5000 }));
      return { ...g.usageSeries, group, rows, previous: rows.map((r) => ({ ...r, costUsd: r.costUsd * 0.75, inputTokens: r.inputTokens * 0.8 })) };
    }],
    ...g.defaultApi(),
  ]);
}

test('heartbeat warnings use the server verdict despite browser clock skew', () => {
  expect(overdue({ ...jobs[0]!, heartbeatAt: ago(100) })).toBe(false);
  expect(overdue({ ...jobs[1]!, heartbeatAt: ago(-100) })).toBe(true);
});

test('queue filters distinguish failures, automatic retries and overdue workers', async ({ page }) => {
  await mockOperations(page);
  await page.goto(`${account}/queue`);
  const rows = page.locator('.queue-table tbody tr');
  await expect(rows).toHaveCount(5);
  await page.getByRole('radio', { name: 'Needs attention (3)' }).click();
  await expect(rows).toHaveCount(3);
  await expect(rows.first()).toContainText('Heartbeat overdue');
  await expect(rows.nth(1)).toContainText('Retries automatically');
  await expect(rows.nth(1).getByRole('button')).toHaveCount(0);
  await page.getByRole('searchbox', { name: 'Search jobs' }).fill('104');
  await expect(rows).toHaveCount(1);
  await expect(rows.first().getByRole('button', { name: 'Re-run' })).toBeVisible();
  await page.getByRole('searchbox').fill('no matches');
  await expect(page.getByText('No jobs match these filters.')).toBeVisible();
});

test('queue actions use existing account-scoped endpoints and confirm before sending', async ({ page }) => {
  await mockOperations(page);
  const sent = await g.mockWrites(page, [
    ['POST', /\/reviews\/rev-1\/cancel$/, { status: 202, body: g.accepted }],
    ['POST', /\/pulls\/alpha\/one\/7\/rerun$/, g.apiError(409, 'already_queued', 'A review is already queued')],
  ]);
  await page.goto(`${account}/queue`);
  await page.locator('.queue-table tbody tr').first().getByRole('button', { name: 'Cancel review' }).click();
  expect(sent).toHaveLength(0);
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel review' }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0]!.url.pathname).toBe(`/api/v1/accounts/${g.SLUG}/reviews/rev-1/cancel`);
  await page.getByRole('button', { name: 'Re-run', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('current head');
  await page.getByRole('dialog').getByRole('button', { name: 'Re-run' }).click();
  await expect(page.getByRole('status')).toContainText('already queued');
});

test('queue members can inspect errors but cannot mutate jobs', async ({ page }) => {
  await mockOperations(page);
  await page.route('**/api/v1/me', (r) => r.fulfill({ json: { ...g.me, admin: false } }));
  await page.goto(`${account}/queue`);
  await expect(page.locator('.queue-actions')).toHaveCount(0);
  await page.getByText('Details', { exact: true }).nth(3).click();
  await expect(page.locator('details[open] .error-text')).toHaveText('Runner exited before publishing its result');
});

test('spending drilldown keeps its period and carries repository and model filters', async ({ page }) => {
  const seen = await mockOperations(page);
  await page.goto(`${account}/usage`);
  await page.getByRole('radio', { name: '7 days', exact: true }).click();
  await page.getByRole('button', { name: 'All repositories' }).click();
  await page.getByRole('button', { name: 'alpha/one →' }).click();
  await expect(page.getByRole('heading', { name: 'API spend by model' })).toBeVisible();
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/usage')).at(-1)?.searchParams.get('repo')).toBe('alpha/one');
  await page.getByRole('button', { name: 'acme/large →' }).click();
  const run = page.getByRole('link', { name: 'Review rev-1 →' });
  await expect(run).toHaveAttribute('href', `#/a/${g.SLUG}/reviews/rev-1/usage`);
  const query = seen.filter((u) => u.pathname.endsWith('/usage')).at(-1)!.searchParams;
  expect(query.get('model')).toBe('acme/large');
  expect(query.get('repo')).toBe('alpha/one');
  expect(query.get('from')).toContain('2026-10-05');
  await page.getByRole('button', { name: 'All repositories' }).click();
  await page.getByRole('button', { name: '(none) →' }).click();
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/usage')).at(-1)?.searchParams.get('repo')).toBe('');
  await page.getByRole('button', { name: 'Clear filters' }).click();
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/usage')).at(-1)?.searchParams.has('repo')).toBe(false);
});

test('failed drilldown does not relabel the preceding selection as the new one', async ({ page }) => {
  await mockOperations(page);
  await page.goto(`${account}/usage?group=repo`);
  await page.route('**/usage?**group=model**', (r) => r.fulfill({ status: 503, json: { code: 'unavailable', message: 'Usage unavailable' } }));
  await page.getByRole('button', { name: 'alpha/one →' }).click();
  await expect(page.getByRole('alert')).toContainText('Usage unavailable');
  await expect(page.getByRole('region', { name: 'Selected usage' })).toHaveCount(0);
  await page.unroute('**/usage?**group=model**');
  await page.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'API spend by model' })).toBeVisible();
});

test('spending filters survive reload and Back from a review', async ({ page }) => {
  await mockOperations(page);
  await page.goto(`${account}/usage?group=review&repo=alpha%2Fone&model=acme%2Flarge&days=7`);
  const run = page.getByRole('link', { name: 'Review rev-1 →' });
  await expect(run).toBeVisible();
  await page.reload();
  await expect(page.getByRole('radio', { name: '7 days', exact: true })).toBeChecked();
  await run.click();
  await expect(page.getByRole('link', { name: 'Review on GitHub' })).toBeVisible();
  await page.goBack();
  await expect(run).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Spending drilldown' })).toContainText('acme/large');
});

test('spending changes retain disappeared groups and suppress unpriced comparisons', async ({ page }) => {
  await mockOperations(page);
  const row = g.usageSeries.rows[0]!;
  await page.route('**/usage?*', (r) => r.fulfill({ json: {
    ...g.usageSeries, group: 'repo', rows: [{ ...row, key: 'alpha/new', costUsd: 3 }, { ...row, key: 'alpha/unpriced', unpricedCalls: 1 }],
    previous: [{ ...row, key: 'alpha/old', costUsd: 4 }, { ...row, key: 'alpha/unpriced', costUsd: 1 }],
  } }));
  await page.goto(`${account}/usage?group=repo`);
  const rows = page.locator('tbody tr');
  await expect(rows).toHaveCount(3);
  await expect(rows.filter({ hasText: 'alpha/old' })).toContainText('-100%');
  await expect(rows.filter({ hasText: 'alpha/new' })).toContainText('new');
  await expect(rows.filter({ hasText: 'alpha/unpriced' })).toContainText('Incomplete');
  await expect(page.getByRole('region', { name: 'Selected usage' }).locator('.stat').first().locator('.delta')).toHaveCount(0);
});

test('spending URLs encode model names and distinguish unfiltered from unattributed', () => {
  const route = { name: 'usage', slug: g.SLUG, filter: { repo: '', model: 'acme/a+b & c', days: '7', group: 'review' } } as const;
  expect(parse(href(route))).toEqual(route);
  expect(usageFilter({ days: 'invalid', group: 'bad', metric: 'bad', billing: 'bad' })).toBeUndefined();
  expect(usageFilter({ repo: '' })).toEqual({ repo: '' });
});

test('operational trends compare rates and omit buckets without outcomes', async ({ page }) => {
  await mockOperations(page);
  await page.route('**/analytics?*', (r) => r.fulfill({ json: { ...analytics, current: { ...analytics.current, reviews: 8, failed: 2 }, previous: { ...analytics.previous, reviews: 9, failed: 1 }, series: [...analytics.series, { ...analytics.series[0], key: '2026-10-15', reviews: 0, failed: 0, medianReviewMs: null }] } }));
  await page.goto(account);
  const rate = page.getByRole('region', { name: 'Operational metrics' }).locator('.stat').filter({ has: page.getByText('Failure rate', { exact: true }) });
  await expect(rate).toContainText('20.0%');
  await expect(rate.locator('.delta')).toContainText('10');
  const trends = page.getByRole('region', { name: 'Operational trends' });
  await trends.getByRole('radio', { name: 'Table', exact: true }).click();
  await expect(trends.locator('tbody tr')).toHaveCount(14);
  await trends.getByRole('radio', { name: 'Duration', exact: true }).click();
  await expect(trends.locator('tbody tr')).toHaveCount(14);
  await trends.getByRole('radio', { name: 'Tokens', exact: true }).click();
  await expect(trends.locator('tbody tr')).toHaveCount(15);
});

test('review handoff links to GitHub and preserves the internal investigation tabs', async ({ page }) => {
  await mockOperations(page);
  await page.goto(`${account}/reviews/rev-1`);
  await expect(page.getByRole('link', { name: 'Review on GitHub' })).toHaveAttribute('href', g.reviewDetail.review.pull.url);
  await expect(page.getByRole('link', { name: 'Findings & code' })).toBeVisible();
  await expect(page.getByRole('link', { name: /Thread on GitHub/ }).first()).toHaveAttribute('href', /#discussion_r/);
});

for (const theme of ['light', 'dark'] as const) {
  test(`operations screenshots in ${theme}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' });
    await page.setViewportSize({ width: 1536, height: 1100 });
    await mockOperations(page);
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Operations overview' })).toBeVisible();
    await expect(page.getByText('No webhooks received')).toBeVisible();
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: testInfo.outputPath(`operations-overview-${theme}.png`) });
    await page.goto(account);
    await expect(page.getByRole('region', { name: 'Operational metrics' })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`operations-analytics-${theme}.png`) });
    await page.goto(`${account}/usage`);
    await page.getByRole('button', { name: 'All repositories' }).click();
    await page.getByRole('button', { name: 'alpha/one →' }).click();
    await expect(page.getByRole('heading', { name: 'API spend by model' })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`operations-spend-${theme}.png`) });
    await page.getByRole('button', { name: 'acme/large →' }).click();
    await expect(page.getByRole('heading', { name: 'API spend by review run' })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`operations-review-usage-${theme}.png`) });
    await page.goto(`${account}/queue`);
    await expect(page.locator('.queue-table tbody tr')).toHaveCount(5);
    await page.screenshot({ path: testInfo.outputPath(`operations-queue-${theme}.png`) });
    await page.goto(`${account}/reviews/rev-1`);
    await expect(page.getByRole('link', { name: 'Review on GitHub' })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`operations-github-${theme}.png`) });
    await page.goto(`${account}/queue`);
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByRole('searchbox')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`operations-queue-mobile-${theme}.png`) });
  });
}

test('queue confirmation survives a poll that removes every job', async ({ page }) => {
  await page.clock.install({ time: now });
  await mockOperations(page);
  let current = jobs;
  await page.route('**/api/v1/accounts/**/queue', (r) => r.fulfill({ json: current }));
  const sent = await g.mockWrites(page, [['POST', /\/reviews\/rev-1\/cancel$/, { status: 202, body: g.accepted }]]);
  await page.goto(`${account}/queue`);
  await page.getByRole('radio', { name: 'Running (2)' }).click();
  await page.locator('.queue-table tbody tr').first().getByRole('button', { name: 'Cancel review' }).click();
  current = [];
  await page.clock.fastForward(15_000);
  await expect(page.locator('.queue-table tbody tr')).toHaveCount(0);
  await expect(page.getByRole('dialog')).toContainText('job #101');
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel review' }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0]!.url.pathname).toBe(`/api/v1/accounts/${g.SLUG}/reviews/rev-1/cancel`);
});

test('review groups omit prior runs and keep the remainder out of review links', async ({ page }) => {
  await mockOperations(page);
  const row = g.usageSeries.rows[0]!;
  await page.route('**/usage?*', (r) => r.fulfill({ json: {
    ...g.usageSeries, group: 'review', rows: [{ ...row, key: 'current-review' }, { ...row, key: '__other__' }],
    previous: [{ ...row, key: 'previous-review' }],
  } }));
  await page.goto(`${account}/usage?group=review`);
  await expect(page.locator('tbody tr')).toHaveCount(2);
  await expect(page.getByRole('columnheader', { name: 'Change' })).toHaveCount(0);
  await expect(page.locator('tbody').getByRole('link')).toHaveCount(1);
  await expect(page.locator('tbody')).toContainText('Other reviews');
  await expect(page.getByText('previous-review')).toHaveCount(0);
});

test('token comparisons sort by tokens and include unpriced calls', async ({ page }) => {
  await mockOperations(page);
  const row = { ...g.usageSeries.rows[0]!, costUsd: 0, outputTokens: 0, calls: 1, planCalls: 1 };
  await page.route('**/api/v1/accounts', (r) => r.fulfill({ json: [{ ...g.accountSummary, chatgptEnabled: true }] }));
  await page.route('**/usage?*', (r) => r.fulfill({ json: {
    ...g.usageSeries, group: 'model', rows: [{ ...row, key: 'a/small', inputTokens: 100 }, { ...row, key: 'z/large', inputTokens: 400, unpricedCalls: 1 }],
    previous: [{ ...row, key: 'z/large', inputTokens: 200 }],
  } }));
  await page.goto(`${account}/usage?group=model&billing=chatgpt&metric=tokens`);
  await expect(page.locator('tbody tr').first()).toContainText('z/large');
  await expect(page.locator('tbody tr').first().locator('td').last()).toHaveText('+100%');
});

test('analytics spending link preserves the selected period', async ({ page }) => {
  await mockOperations(page);
  await page.goto(account);
  await page.getByRole('radio', { name: '90 days', exact: true }).click();
  await page.getByRole('link', { name: /Investigate spending/ }).click();
  await expect(page.getByRole('radio', { name: '90 days', exact: true })).toBeChecked();
});

test('background usage refresh leaves drilldown available and cannot overwrite a new scope', async ({ page }) => {
  await page.addInitScript(() => {
    window.EventSource = class extends EventTarget {
      constructor() {
        super();
        window.addEventListener('refresh-usage', () => this.dispatchEvent(new MessageEvent('model_call', { data: JSON.stringify({ kind: 'model_call', account: 'github/alpha' }) })));
      }
      close() {}
    } as unknown as typeof EventSource;
  });
  await mockOperations(page);
  await page.goto(`${account}/usage?group=repo`);
  const drill = page.getByRole('button', { name: 'alpha/one →' });
  await expect(drill).toBeVisible();
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let pending = false;
  await page.route('**/usage?**group=repo**', async (r) => { pending = true; await held; await r.fulfill({ json: { ...g.usageSeries, group: 'repo' } }); });
  await page.evaluate(() => window.dispatchEvent(new Event('refresh-usage')));
  await expect.poll(() => pending).toBe(true);
  await expect(drill).toBeEnabled();
  await drill.click();
  await expect(page.getByRole('heading', { name: 'API spend by model' })).toBeVisible();
  const refreshed = page.waitForResponse((r) => r.url().includes('/usage?') && new URL(r.url()).searchParams.get('group') === 'repo');
  release();
  await refreshed;
  await expect(page.getByRole('button', { name: 'acme/large →' })).toBeVisible();
});
