import { test, expect } from './fixtures';
import * as g from './golden';
import { href, parse, queueFilter, repoFilter } from '../src/lib/routes';
import { pageIn, reportingRoute } from '../src/lib/sections';
import type { Job, Repository } from '../src/lib/types';

const account = `/#/a/${g.SLUG}`;
const now = Date.parse('2026-10-12T16:00:00Z');
const jobs: Job[] = [
  { ...g.job, id: 101, state: 'running', attemptedAt: new Date(now - 120_000).toISOString(), heartbeatAt: new Date(now - 10_000).toISOString(), lastError: '', cause: '', cancelableReviewId: 'rev-1' },
  { ...g.job, id: 102, state: 'retryable' },
  { ...g.job, id: 103, state: 'discarded', recentFailure: true, args: { ...g.job.args, repository: 'alpha/platform', number: 29 }, finalizedAt: new Date(now - 300_000).toISOString(), lastError: 'Runner exceeded its deadline.\nLast output: waiting for a model slot.', cause: '' },
];

async function mock(page: Parameters<typeof g.mockApi>[0]) {
  await page.clock.setFixedTime(now);
  return g.mockApi(page, [
    [/\/api\/v1\/me$/, { ...g.me, accounts: [g.SLUG, 'github/beta'] }],
    [/\/queue$/, (u: URL) => u.pathname === '/api/v1/queue' ? { ...g.instanceQueue, jobs: jobs.map((j) => ({ ...j, account: g.SLUG })) } : jobs],
    [/\/usage$/, (u: URL) => ({ ...g.usageSeries, group: u.searchParams.get('group') ?? 'day', rows: ['alpha/one', 'alpha/platform', 'alpha/services'].map((key, i) => ({ ...g.usageSeries.rows[0]!, key: u.searchParams.get('group') === 'repo' ? key : `2026-10-${10 + i}`, calls: 80 - 20 * i, costUsd: 12 - 3 * i, inputTokens: 900000 - 200000 * i, outputTokens: 80000 - 10000 * i })) })],
    ...g.defaultApi(),
  ]);
}

test('queue and repository URLs round-trip reserved text and sanitize unsupported filters', () => {
  const queue = { name: 'queue', slug: g.SLUG, filter: { state: 'attention', q: 'a/b + c&d' } } as const;
  const repo = { name: 'repos', slug: g.SLUG, filter: { type: 'forks', q: 'a/b + c&d' } } as const;
  expect(parse(href(queue))).toEqual(queue);
  expect(parse(href(repo))).toEqual(repo);
  expect(parse(href({ name: 'instanceQueue', filter: queue.filter }))).toEqual({ name: 'instanceQueue', filter: queue.filter });
  expect(queueFilter({ state: 'nonsense', q: '' })).toBeUndefined();
  expect(repoFilter({ type: 'nonsense', q: '' })).toBeUndefined();
});

test('switching reporting pages or accounts retains dates without leaking repository filters', () => {
  const usage = { name: 'usage', slug: g.SLUG, filter: { days: '90', metric: 'tokens', repo: 'alpha/one', model: 'private/model', billing: 'chatgpt' } } as const;
  expect(pageIn(usage, 'github/beta')).toEqual({ name: 'usage', slug: 'github/beta', filter: { days: '90', metric: 'tokens' } });
  expect(reportingRoute({ name: 'analytics', slug: g.SLUG }, usage)).toEqual({ name: 'analytics', slug: g.SLUG, filter: { days: '90' } });
  expect(reportingRoute({ name: 'analytics', slug: g.SLUG }, { ...usage, filter: { days: '1' } })).toEqual({ name: 'analytics', slug: g.SLUG, filter: { days: '1' } });
});

test('queue filters survive reload and returning from a pull request', async ({ page }) => {
  await mock(page);
  await page.goto(`${account}/queue`);
  await page.getByRole('radio', { name: 'Waiting (1)' }).click();
  await page.getByRole('searchbox', { name: 'Search jobs' }).fill('alpha/one');
  await expect(page).toHaveURL(/queue\?state=waiting&q=alpha%2Fone$/);
  await page.reload();
  await expect(page.getByRole('radio', { name: 'Waiting (1)' })).toBeChecked();
  await expect(page.locator('.queue-table tbody tr')).toHaveCount(1);
  await page.locator('.queue-table').getByRole('link', { name: 'alpha/one#7' }).click();
  await page.goBack();
  await expect(page.getByRole('searchbox', { name: 'Search jobs' })).toHaveValue('alpha/one');
  await expect(page.getByRole('radio', { name: 'Waiting (1)' })).toBeChecked();
});

test('repository filters, loaded pages and scroll return after opening a repository', async ({ page }) => {
  await mock(page);
  const repos: Repository[] = Array.from({ length: 160 }, (_, i) => ({ ...g.repoPage.items[0]!, id: `repo-${i}`, fullName: `alpha/repo-${i}`, fork: true }));
  await page.route('**/api/v1/accounts/**/repos?*', (r) => {
    const q = new URL(r.request().url()).searchParams;
    const start = Number(q.get('cursor') ?? 0);
    const end = Math.min(start + Number(q.get('limit') ?? 100), repos.length);
    return r.fulfill({ json: g.pageOf(repos.slice(start, end), end < repos.length ? String(end) : null) });
  });
  await page.route('**/repos/alpha/repo-*', (r) => r.fulfill({ json: g.repoDetail }));
  await page.goto(`${account}/repos`);
  await page.getByRole('combobox', { name: 'Type' }).selectOption('forks');
  await page.getByPlaceholder('Filter by name').fill('repo-');
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(160);
  const link = page.locator('.repo-table').getByRole('link', { name: 'alpha/repo-145', exact: true });
  await link.scrollIntoViewIfNeeded();
  const top = await page.locator('main.page').evaluate((el) => el.scrollTop);
  expect(top).toBeGreaterThan(1000);
  await link.click();
  await expect(page.getByRole('heading', { name: 'alpha/repo-145' })).toBeVisible();
  await page.goBack();
  await expect(page.getByPlaceholder('Filter by name')).toHaveValue('repo-');
  await expect(page.getByRole('combobox', { name: 'Type' })).toHaveValue('forks');
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(160);
  await expect.poll(async () => Math.abs(await page.locator('main.page').evaluate((el) => el.scrollTop) - top)).toBeLessThan(3);
  await page.reload();
  await expect(page.getByPlaceholder('Filter by name')).toHaveValue('repo-');
  await expect(page.getByRole('combobox', { name: 'Type' })).toHaveValue('forks');
});

test('reporting period follows sidebar navigation and account switching', async ({ page }) => {
  await mock(page);
  await page.goto(`${account}/analytics?days=90`);
  const nav = page.locator('.desktop-navigation');
  await nav.getByRole('link', { name: 'Usage & costs', exact: true }).click();
  await expect(page.getByRole('radio', { name: '90 days', exact: true })).toBeChecked();
  await nav.getByRole('link', { name: 'Analytics', exact: true }).click();
  await expect(page).toHaveURL(/\?days=90$/);
  await page.locator('.account-button').click();
  await page.getByRole('navigation', { name: 'Scope' }).getByRole('link', { name: 'github/beta' }).click();
  await expect(page).toHaveURL(/github\/beta\/analytics\?days=90$/);
  await expect(page.getByRole('radio', { name: '90 days', exact: true })).toBeChecked();
});

test('usage puts the selected chart before monthly limits and help is keyboard accessible', async ({ page }) => {
  await mock(page);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`${account}/usage?group=repo`);
  const chart = page.getByRole('region', { name: 'API spend by repository' });
  const limits = page.getByRole('region', { name: 'This month' });
  expect((await chart.boundingBox())!.y).toBeLessThan(450);
  expect((await limits.boundingBox())!.y).toBeGreaterThan((await chart.boundingBox())!.y);
  const help = page.getByRole('button', { name: 'About usage' });
  await help.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog', { name: 'About usage' })).toContainText('Cache detail');
  await page.keyboard.press('Escape');
  await expect(help).toBeFocused();
  await expect(page.getByRole('dialog', { name: 'About usage' })).toHaveCount(0);
});

for (const theme of ['light', 'dark'] as const) {
  test(`navigation cleanup screenshots in ${theme}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await mock(page);
    await page.goto(`${account}/usage?group=repo&days=7`);
    await expect(page.getByRole('region', { name: 'Selected usage' })).toBeVisible();
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: testInfo.outputPath(`usage-${theme}.png`) });
    await page.goto(`${account}/queue`);
    await expect(page.locator('.queue-table tbody tr')).toHaveCount(3);
    await expect(page.locator('.desktop-navigation [aria-current="page"]')).toHaveText('Queue');
    await expect(page.getByText('Runner exceeded its deadline.', { exact: true })).toBeVisible();
    await expect(page.getByText('Last output: waiting for a model slot.', { exact: true })).toBeHidden();
    await page.screenshot({ path: testInfo.outputPath(`queue-${theme}.png`) });
    await page.getByLabel('Details for job #103').click();
    await expect(page.locator('.job-details[open]')).toContainText('Last output: waiting for a model slot.');
    await page.screenshot({ path: testInfo.outputPath(`queue-details-${theme}.png`) });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: 'Open navigation' }).click();
    await expect(page.getByRole('dialog', { name: 'Navigation' })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`navigation-mobile-${theme}.png`) });
    await page.getByRole('dialog', { name: 'Navigation' }).getByRole('link', { name: 'Usage & costs' }).click();
    await expect(page.getByRole('heading', { name: 'Usage & costs' })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
    await page.screenshot({ path: testInfo.outputPath(`usage-mobile-${theme}.png`) });
  });
}
