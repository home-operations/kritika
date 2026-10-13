import { test, expect } from '@playwright/test';
import * as g from './golden';

test('all-accounts analytics adds the accounts up, without medians, and names each repository\'s account', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto('/#/analytics');
  await expect(page.locator('main h1')).toHaveText('Analytics');
  const stats = page.getByRole('region', { name: 'Totals' });
  await expect(stats.locator('.stat .stat-label')).toHaveText(['Reviews', 'Addressed', 'Reactions', 'API spend']);
  await expect(page.getByRole('combobox', { name: 'Trend metric' }).locator('option')).toHaveText(['Reviews', 'Findings by severity', 'Failure rate', 'Tokens', 'API spend']);
  const repos = page.getByRole('region', { name: 'Most reviewed repositories' });
  await expect(repos.getByRole('columnheader')).toHaveText(['Repository', 'Account', 'Reviews', 'Findings', 'Addressed']);
  await expect(repos.getByRole('link', { name: g.SLUG, exact: true })).toHaveAttribute('href', `#/a/${g.SLUG}`);
  await expect(repos.getByRole('link', { name: 'alpha/one', exact: true })).toHaveAttribute('href', `#/a/${g.SLUG}/repos/alpha/one`);
  await page.getByRole('radio', { name: '7 days' }).click();
  await expect(page).toHaveURL(/#\/analytics\?days=7$/);
  expect(seen.some((u) => u.pathname === '/api/v1/analytics')).toBe(true);
  expect(seen.some((u) => u.pathname.includes('/accounts/') && u.pathname.endsWith('/analytics'))).toBe(false);
  await page.getByRole('link', { name: 'Investigate spending →' }).click();
  await expect(page).toHaveURL(/#\/usage\?days=7$/);
});

test('all-accounts usage groups by account, with uncapped month tiles and a mean per review', async ({ page }) => {
  const seen = await g.mockApi(page, [
    [/\/api\/v1\/accounts$/, [g.accountSummary, { ...g.accountSummary, slug: 'github/beta' }]],
    [/\/api\/v1\/usage$/, (u: URL) => u.searchParams.get('group') === 'account'
      ? { ...g.usageSeries, group: 'account', rows: [{ ...g.usageSeries.rows[0]!, key: g.SLUG, account: g.SLUG }, { ...g.usageSeries.rows[0]!, key: 'github/beta', account: 'github/beta', costUsd: 0.05 }] }
      : g.usageSeries],
    ...g.defaultApi(),
  ]);
  await page.goto('/#/usage');
  await expect(page.locator('main h1')).toHaveText('Usage & costs');
  const totals = page.getByRole('region', { name: 'Totals' });
  await expect(totals.locator('.stat').filter({ hasText: 'API spend this month' })).toContainText('$3.00');
  await expect(totals.locator('.stat').filter({ hasText: 'API spend this month' })).toContainText('$0.10 per review, mean of 24');
  await expect(totals.locator('.stat').filter({ hasText: 'Tokens this month' })).toContainText('no cap');
  await expect(totals.getByRole('meter')).toHaveCount(0);
  await page.getByRole('combobox', { name: 'Group by' }).selectOption('account');
  await expect.poll(() => seen.some((u) => u.pathname === '/api/v1/usage' && u.searchParams.get('group') === 'account')).toBe(true);
  await expect(page.getByRole('columnheader', { name: 'Account' })).toBeVisible();
  await expect(page.getByRole('link', { name: `${g.SLUG} →` })).toHaveAttribute('href', `#/a/${g.SLUG}/usage`);
  await expect(page.getByRole('link', { name: 'github/beta →' })).toHaveAttribute('href', '#/a/github/beta/usage');
  expect(seen.some((u) => u.pathname.includes('/accounts/') && u.pathname.endsWith('/usage'))).toBe(false);
});

test('the account pages keep their medians, caps and findings links', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto(`/#/a/${g.SLUG}/analytics`);
  await expect(page.getByRole('region', { name: 'Totals' }).locator('.stat .stat-label')).toHaveText(['Reviews', 'Median review', 'Time to merge', 'Addressed', 'Reactions', 'API spend']);
  await expect(page.getByRole('combobox', { name: 'Group by' })).toHaveCount(0);
  await page.goto(`/#/a/${g.SLUG}/usage`);
  await expect(page.getByRole('combobox', { name: 'Group by' }).locator('option')).not.toContainText(['Account']);
});
