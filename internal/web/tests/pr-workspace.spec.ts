import { test, expect } from '@playwright/test';
import * as g from './golden';
import type { ReviewDetail, ReviewStatus } from '../src/lib/types';

const pullURL = `/#/a/${g.SLUG}/pulls/alpha/one/7`;
const reviewURL = `/#/a/${g.SLUG}/reviews/rev-1`;

test.beforeEach(async ({ page }) => { await g.mockApi(page, g.defaultApi()); });

test('PR workspace keeps latest review, code, activity and diagnostics under one header', async ({ page }) => {
  await page.goto(pullURL);
  await expect(page.locator('h1')).toHaveText(g.pullDetail.pull.title);
  const nav = page.getByRole('navigation', { name: 'Review sections' });
  await expect(nav.getByRole('link')).toHaveText(['Overview', /Findings & code/, 'Activity']);
  await expect(page.getByLabel('Review run')).toHaveValue('');
  await expect(page.getByRole('navigation', { name: 'Run sections' })).toHaveCount(0);
  await nav.getByRole('link', { name: 'Findings & code', exact: true }).click();
  await expect(page).toHaveURL(/pulls\/alpha\/one\/7\?tab=diff$/);
  await page.getByRole('button', { name: /Next finding/ }).click();
  await expect(page).toHaveURL(new RegExp(`finding=${g.reviewDetail.findings[0]!.id}$`));
  await page.reload();
  await expect(page.locator(`#diff-finding-${g.reviewDetail.findings[0]!.id}`)).toBeFocused();
  await nav.getByRole('link', { name: 'Activity', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Review history' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Follow-ups' })).toBeVisible();
  await nav.getByRole('combobox', { name: 'Run details' }).selectOption('timeline');
  await expect(page.getByRole('navigation', { name: 'Run sections' })).toHaveCount(0);
  await expect(page.getByRole('combobox', { name: 'Run details' })).toHaveValue('timeline');
  await expect(page.locator('h1')).toHaveText(g.pullDetail.pull.title);
});

test('history selection is linkable and browser Back restores the latest run', async ({ page }) => {
  const older: ReviewDetail = { ...g.reviewDetail, review: { ...g.reviewDetail.review, id: 'old', newestReviewId: 'rev-1', headSha: '1234567' }, summary: { ...g.reviewDetail.summary!, take: 'Older review conclusion' } };
  await g.mockApi(page, [
    [/\/reviews\/old$/, older],
    [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [g.reviewDetail.review, older.review] }],
    ...g.defaultApi(),
  ]);
  await page.goto(pullURL);
  await page.getByLabel('Review run').selectOption('old');
  await expect(page).toHaveURL(/run=old&tab=summary$/);
  await expect(page.locator('.newer-notice')).toContainText('Historical review');
  await expect(page.getByRole('region', { name: 'Review summary', exact: true })).toContainText('Older review conclusion');
  await page.reload();
  await expect(page.getByLabel('Review run')).toHaveValue('old');
  await page.goBack();
  await expect(page.getByLabel('Review run')).toHaveValue('');
  await expect(page.locator('.newer-notice')).toHaveCount(0);
});

test('a run from another PR cannot appear inside the selected PR workspace', async ({ page }) => {
  await g.mockApi(page, [[/\/reviews\/other$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, id: 'other', pull: { ...g.reviewDetail.review.pull, number: 8 } } }], ...g.defaultApi()]);
  await page.goto(`${pullURL}?run=other`);
  await expect(page.getByRole('alert')).toContainText('different pull request');
  await expect(page.getByRole('navigation', { name: 'Review sections' })).toHaveCount(0);
  await page.getByLabel('Review run').selectOption('');
  await expect(page.getByRole('navigation', { name: 'Review sections' })).toBeVisible();
});

for (const status of ['running', 'failed', 'capped', 'completed'] satisfies ReviewStatus[]) {
  test(`execution and findings stay distinct for ${status} reviews`, async ({ page }) => {
    await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, findings: [], summary: null, review: { ...g.reviewDetail.review, status, error: status === 'failed' ? 'Provider unavailable' : '' } }], ...g.defaultApi()]);
    await page.goto(reviewURL);
    const outcome = page.getByLabel('Review outcome');
    if (status === 'completed') await expect(outcome).toHaveCount(0);
    else {
      await expect(outcome).toContainText(status === 'running' ? 'not final' : 'not a clean review');
      await expect(page.getByText('No findings.', { exact: true })).toHaveCount(0);
    }
    if (status === 'failed') {
      await expect(page.getByRole('note').filter({ hasText: 'Execution problem' })).toContainText('Provider unavailable');
      await page.getByRole('link', { name: 'Inspect run details' }).click();
      await expect(page.getByRole('combobox', { name: 'Run details' })).toHaveValue('timeline');
    }
  });
}

test('focus mode preserves the selected finding, filters and navigator', async ({ page }) => {
  await page.goto(`${pullURL}?tab=diff`);
  await page.getByRole('searchbox', { name: 'Search files' }).fill('a.go');
  await page.getByRole('button', { name: 'Next finding' }).click();
  await page.getByRole('button', { name: 'Hide navigator' }).click();
  await expect(page.getByRole('navigation', { name: 'Findings and files' })).toBeHidden();
  await expect(page.locator('.review-workspace')).toHaveClass(/code-focused/);
  await page.getByRole('button', { name: 'Show navigator' }).click();
  await expect(page.getByRole('searchbox', { name: 'Search files' })).toHaveValue('a.go');
  await expect(page.locator('.finding-jump.active')).toHaveCount(1);
});

for (const width of [390, 1440]) {
  test(`finding inspector preserves filtered results and keyboard focus at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const findings = [g.accountFinding, { ...g.accountFinding, id: 'f-2', title: 'Second finding' }];
    await g.mockApi(page, [[/\/findings$/, g.pageOf(findings)], ...g.defaultApi()]);
    await page.goto(`/#/a/${g.SLUG}/findings?severity=p1`);
    const opener = page.locator('.finding-link').first();
    await opener.click();
    const panel = page.getByRole('dialog', { name: 'Finding details' });
    await expect(panel).toContainText(findings[0]!.explanation);
    await expect(panel.getByRole('button', { name: 'Previous' })).toBeDisabled();
    await page.keyboard.press('j');
    await expect(panel).toContainText('Second finding');
    await expect(panel.getByRole('button', { name: 'Next', exact: false })).toBeDisabled();
    await page.keyboard.press('k');
    await expect(panel).toContainText(findings[0]!.title);
    await expect(panel.getByRole('link', { name: 'Inspect in code' })).toHaveAttribute('href', `#/a/${g.SLUG}/reviews/${findings[0]!.reviewId}/diff?finding=${findings[0]!.id}`);
    const bounds = (await panel.boundingBox())!;
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width + 1);
    await page.screenshot({ path: testInfo.outputPath(`finding-inspector-${width}.png`) });
    await page.keyboard.press('Escape');
    await expect(panel).toHaveCount(0);
    await expect(opener).toBeFocused();
    await expect(page).toHaveURL(/findings\?severity=p1$/);
    await expect(page.locator('.finding-row')).toHaveCount(2);
  });
}
