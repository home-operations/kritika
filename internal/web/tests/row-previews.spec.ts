import { test, expect } from './fixtures';
import * as g from './golden';
import AxeBuilder from '@axe-core/playwright';

const account = `/#/a/${g.SLUG}`;
const dark = { ...g.me, settings: { ...g.me.settings, theme: 'dark' as const } };

test('pull previews load on demand, preserve navigation and reuse cached details', async ({ page }) => {
  const seen = await g.mockApi(page, [[/\/api\/v1\/me$/, dark], ...g.defaultApi()]);
  await page.goto(`${account}/pulls`);
  const toggle = page.getByRole('button', { name: 'Preview alpha/one#7', exact: true });
  const preview = page.getByRole('region', { name: 'Preview of alpha/one#7' });
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  expect(seen.filter((u) => u.pathname.endsWith('/reviews/rev-1'))).toHaveLength(0);
  await toggle.focus();
  await page.keyboard.press('Enter');
  await expect(preview).toContainText(g.reviewDetail.summary!.take);
  await expect(page).toHaveURL(/\/pulls$/);
  await expect(toggle).toBeFocused();
  await toggle.hover();
  await expect(toggle).toHaveCSS('text-decoration-line', 'none');
  const calls = seen.filter((u) => u.pathname.endsWith('/reviews/rev-1')).length;
  await toggle.click();
  await expect(preview).toHaveCount(0);
  await toggle.click();
  await expect(preview).toContainText(g.reviewDetail.summary!.take);
  expect(seen.filter((u) => u.pathname.endsWith('/reviews/rev-1'))).toHaveLength(calls);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await preview.getByRole('link', { name: g.reviewDetail.findings[0]!.title }).click();
  await expect(page).toHaveURL(/\/pulls\/alpha\/one\/7\?run=rev-1&tab=diff&finding=/);
});

test('pull preview failures can be retried without leaving the list', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  let failed = true;
  await page.route('**/reviews/rev-1', (route) => route.fulfill(failed
    ? { status: 503, json: { error: { code: 'unavailable', message: 'Review unavailable' } } }
    : { json: g.reviewDetail }));
  await page.goto(`${account}/pulls`);
  await page.getByRole('button', { name: 'Preview alpha/one#7', exact: true }).click();
  const preview = page.getByRole('region', { name: 'Preview of alpha/one#7' });
  await expect(preview.getByRole('alert')).toBeVisible();
  failed = false;
  await preview.getByRole('button', { name: 'Retry' }).click();
  await expect(preview).toContainText(g.reviewDetail.summary!.take);
  await expect(page.locator('.pull-row')).toHaveCount(1);
});

test('previews stay independent for matching pull numbers in different accounts', async ({ page }) => {
  const beta = 'github/beta';
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, { ...dark, accounts: [g.SLUG, beta] }],
    [/\/api\/v1\/pulls$/, g.pageOf([{ ...g.pull, account: g.SLUG }, { ...g.pull, account: beta }])],
    [/\/accounts\/github\/beta\/reviews\/rev-1$/, { ...g.reviewDetail, summary: { ...g.reviewDetail.summary!, headline: 'Beta review summary' } }],
    ...g.defaultApi(),
  ]);
  await page.goto('/#/pulls');
  await page.getByRole('button', { name: `Preview alpha/one#7 in ${beta}`, exact: true }).click();
  await expect(page.locator('.row-preview')).toHaveCount(1);
  await expect(page.locator('.row-preview')).toContainText('Beta review summary');
  await page.getByRole('button', { name: `Preview alpha/one#7 in ${g.SLUG}`, exact: true }).click();
  await expect(page.locator('.row-preview')).toHaveCount(2);
  await expect(page.locator('.row-preview').first()).toContainText(g.reviewDetail.summary!.take);
  await expect(page.locator('.row-preview').last()).toContainText('Beta review summary');
});

for (const status of ['running', 'failed', 'skipped'] as const) {
  test(`${status} previews do not imply a clean review`, async ({ page }) => {
    await g.mockApi(page, [
      [/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status }, summary: null, findings: [] }],
      ...g.defaultApi(),
    ]);
    await page.goto(`${account}/pulls`);
    await page.getByRole('button', { name: 'Preview alpha/one#7', exact: true }).click();
    const preview = page.getByRole('region', { name: 'Preview of alpha/one#7' });
    await expect(preview).toContainText(status === 'running' ? 'Review in progress' : status === 'skipped' ? 'No review performed' : 'Review incomplete');
    await expect(preview.getByText('No findings.', { exact: true })).toHaveCount(0);
  });
}

test('unreviewed pulls expand without a review request', async ({ page }) => {
  const seen = await g.mockApi(page, [[/\/pulls$/, g.pageOf([{ ...g.pull, lastReview: null }])], ...g.defaultApi()]);
  await page.goto(`${account}/pulls`);
  await page.getByRole('button', { name: 'Preview alpha/one#7', exact: true }).click();
  const preview = page.getByRole('region', { name: 'Preview of alpha/one#7' });
  await expect(preview).toContainText('Not reviewed yet.');
  expect(seen.some((u) => u.pathname.includes('/reviews/'))).toBe(false);
  await preview.getByRole('link', { name: 'Open pull request' }).click();
  await expect(page).toHaveURL(/\/pulls\/alpha\/one\/7$/);
});

test('repository previews load effective settings on demand without changing selection', async ({ page }) => {
  const seen = await g.mockApi(page, [[/\/api\/v1\/me$/, dark], ...g.defaultApi()]);
  await page.goto(`${account}/repos`);
  const toggle = page.getByRole('button', { name: 'Preview alpha/one', exact: true });
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  expect(seen.some((u) => u.pathname.endsWith('/repos/alpha/one'))).toBe(false);
  await page.getByRole('checkbox', { name: 'Select alpha/one', exact: true }).check();
  await toggle.click();
  const preview = page.getByRole('region', { name: 'Preview of alpha/one', exact: true });
  await expect(preview).toContainText((g.repoDetail.repoConfig?.settings ?? g.repoDetail.settings).models.review);
  await expect(preview).toContainText(g.repoDetail.index.activeCommit!);
  await expect(page.getByRole('checkbox', { name: 'Select alpha/one', exact: true })).toBeChecked();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await preview.getByRole('link', { name: 'Open repository' }).click();
  await expect(page).toHaveURL(/\/repos\/alpha\/one$/);
});

test('desktop dark screenshots show expanded pull, queue and repository rows', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, dark],
    [/\/pulls$/, g.pageOf([g.pull, { ...g.pull, number: 8, title: 'Update widget settings' }, { ...g.pull, number: 9, title: 'Add widget tests' }])],
    [/\/repos$/, g.pageOf([g.repoPage.items[0]!, ...['two', 'three'].map((name) => ({ ...g.repoPage.items[0]!, id: `repo-${name}`, fullName: `alpha/${name}` }))])],
    [/\/accounts\/github\/alpha\/queue$/, [g.job, { ...g.job, id: 43 }, { ...g.job, id: 44 }]],
    ...g.defaultApi(),
  ]);
  let stripeColors: string[] | undefined;
  for (const [name, path, button, region] of [
    ['pulls-expanded', `${account}/pulls`, 'Preview alpha/one#7', 'Preview of alpha/one#7'],
    ['queue-expanded', `${account}/queue`, `Details for job #${g.job.id}`, `Job #${g.job.id} details`],
    ['repositories-expanded', `${account}/repos`, 'Preview alpha/one', 'Preview of alpha/one'],
  ]) {
    await page.goto(path!);
    if (name === 'repositories-expanded') {
      await page.getByRole('checkbox', { name: 'Select alpha/one', exact: true }).check();
    }
    const rows = page.locator('table').filter({ has: page.getByRole('button', { name: button, exact: true }) }).locator('tbody > tr:not(.row-preview)');
    await page.mouse.move(0, 0);
    const colors = await rows.evaluateAll((els) => els.map((el) => getComputedStyle(el).backgroundColor));
    expect(colors).toHaveLength(3);
    expect(colors[0]).toBe(colors[2]);
    expect(colors[0]).not.toBe(colors[1]);
    if (stripeColors) expect(colors).toEqual(stripeColors);
    stripeColors = colors;
    await page.getByRole('button', { name: button, exact: true }).click();
    await page.mouse.move(0, 0);
    expect(await rows.evaluateAll((els) => els.slice(1).map((el) => getComputedStyle(el).backgroundColor))).toEqual(colors.slice(1));
    const preview = page.getByRole('region', { name: region, exact: true });
    await expect(preview).toBeVisible();
    await expect(preview.getByRole('status')).toHaveCount(0);
    await expect(preview.getByRole('alert')).toHaveCount(0);
    await expect(page.locator('html')).toHaveClass(/dark/);
    await page.evaluate(() => document.fonts.ready);
    expect(await page.locator('main').evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.screenshot({ path: testInfo.outputPath(`${name}-desktop-dark.png`) });
  }
});

test('review findings expand with the keyboard and preserve row stripes', async ({ page }, testInfo) => {
  const first = g.reviewDetail.findings[0]!;
  const findings = [first, { ...first, id: 'second', title: 'Check error handling', severity: 'p1' }, { ...first, id: 'third', title: 'Clarify the fallback', severity: 'p2' }];
  await page.setViewportSize({ width: 1440, height: 1000 });
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, dark],
    [/\/reviews\/rev-1$/, { ...g.reviewDetail, findings }],
    ...g.defaultApi(),
  ]);
  await page.goto(`${account}/pulls/alpha/one/7`);
  const rows = page.locator('.review-findings [data-finding]');
  await expect(rows).toHaveCount(3);
  await expect(page.locator('.finding')).toHaveCount(0);
  const colors = await rows.evaluateAll((els) => els.map((el) => getComputedStyle(el).backgroundColor));
  const toggle = rows.first().getByRole('button');
  await toggle.focus();
  await page.keyboard.press('Enter');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.finding')).toContainText(first.explanation);
  await expect(toggle).toBeFocused();
  expect(await rows.evaluateAll((els) => els.slice(1).map((el) => getComputedStyle(el).backgroundColor))).toEqual(colors.slice(1));
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: testInfo.outputPath('review-findings-expanded-desktop-dark.png'), fullPage: true });
  await page.keyboard.press('Enter');
  await expect(page.locator('.finding')).toHaveCount(0);
  await rows.first().getByRole('link').click();
  await expect(page).toHaveURL(new RegExp(`tab=diff&finding=${first.id}$`));
});
