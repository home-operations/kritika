import { test, expect } from './fixtures';
import * as g from './golden';

const account = `/#/a/${g.SLUG}`;
const pullURL = `${account}/pulls/alpha/one/7`;
const findings = Array.from({ length: 3 }, (_, i) => ({ ...g.accountFinding, id: `f-${i + 1}`, title: `Finding ${i + 1}` }));

test.beforeEach(async ({ page }) => { await g.mockApi(page, g.defaultApi()); });

test('mixed-case PR URLs accept their own runs and still reject other PRs', async ({ page }) => {
  await g.mockApi(page, [
    [/\/pulls\/Alpha\/One\/7$/, g.pullDetail],
    [/\/reviews\/wrong$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, pull: { ...g.reviewDetail.review.pull, number: 8 } } }],
    ...g.defaultApi(),
  ]);
  await page.goto(`${account}/pulls/Alpha/One/7`);
  await expect(page.getByRole('navigation', { name: 'Review sections' })).toBeVisible();
  await page.goto(`${account}/pulls/Alpha/One/7?run=wrong`);
  await expect(page.getByRole('alert')).toContainText('different pull request');
});

test('inspector buttons keep their keyboard direction until the boundary', async ({ page }) => {
  await g.mockApi(page, [[/\/findings$/, g.pageOf(findings)], ...g.defaultApi()]);
  await page.goto(`${account}/findings`);
  await page.locator('.finding-link').first().click();
  const panel = page.getByRole('dialog');
  const next = panel.getByRole('button', { name: 'Next' });
  const previous = panel.getByRole('button', { name: 'Previous' });
  await next.focus();
  await page.keyboard.press('Enter');
  await expect(panel).toContainText('Finding 2');
  await expect(next).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(panel).toContainText('Finding 3');
  await expect(previous).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(panel).toContainText('Finding 2');
  await expect(previous).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.locator('.finding-link').nth(1)).toBeFocused();
  await expect(page.locator('.finding-link').nth(1)).toHaveAttribute('aria-current', 'true');
  await page.keyboard.press('Enter');
  await expect(panel).toContainText('Finding 2');
});

test('j then Escape returns to the inspected finding and title links support new tabs', async ({ page, context }) => {
  await g.mockApi(page, [[/\/findings$/, g.pageOf(findings)], ...g.defaultApi()]);
  await page.goto(`${account}/findings?severity=p1`);
  await page.locator('.finding-link').first().click();
  await page.keyboard.press('j');
  await page.keyboard.press('Escape');
  const selected = page.locator('.finding-link').nth(1);
  await expect(selected).toBeFocused();
  await expect(selected).toHaveAttribute('aria-current', 'true');
  await expect(selected).toHaveAttribute('href', `#/a/${g.SLUG}/reviews/${findings[1]!.reviewId}?finding=f-2`);
  const popup = context.waitForEvent('page');
  await selected.click({ modifiers: ['ControlOrMeta'] });
  const opened = await popup;
  await expect(opened).toHaveURL(/reviews\/.*\?finding=f-2$/);
  await opened.close();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page).toHaveURL(/findings\?severity=p1$/);
});

for (const start of ['summary', 'diff']) {
  test(`finding links from ${start} pin their run across a new latest review`, async ({ page }) => {
    await page.goto(`${pullURL}?tab=${start}`);
    if (start === 'summary') await page.locator('.finding-code-link').first().click();
    else await page.getByRole('button', { name: 'Next finding' }).click();
    await expect(page).toHaveURL(/run=rev-1/);
    const link = page.url();
    const newer = { ...g.reviewDetail.review, id: 'rev-2' };
    await g.mockApi(page, [[/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [newer, g.reviewDetail.review] }], ...g.defaultApi()]);
    await page.goto(link);
    await page.reload();
    await expect(page.getByLabel('Review run')).toHaveValue('rev-1');
    await expect(page.locator(`#diff-finding-${g.reviewDetail.findings[0]!.id}`)).toBeFocused();
  });
}

for (const route of ['reviews/skipped', 'pulls/alpha/one/7?run=skipped']) {
  test(`skipped runs explain intent without claiming another run is displayed: ${route}`, async ({ page }) => {
    const skipped = { ...g.reviewDetail.review, id: 'skipped', status: 'skipped' as const, skipReason: 'unchanged_patch' as const };
    await g.mockApi(page, [
      [/\/reviews\/skipped$/, { ...g.reviewDetail, review: skipped, findings: [], summary: null }],
      [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [skipped, g.reviewDetail.review] }],
      ...g.defaultApi(),
    ]);
    await page.goto(`${account}/${route}`);
    await expect(page.getByLabel('Review outcome')).toContainText('No review performed. patch unchanged');
    await expect(page.getByText('No findings recorded. This review was skipped.')).toBeVisible();
    await expect(page.getByText(/Showing the last review|not a clean review|did not complete/)).toHaveCount(0);
  });
}

test('review navigation links to its repository, PR and prior run', async ({ page }) => {
  await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, priorReviewId: 'older' } }], ...g.defaultApi()]);
  await page.goto(`${account}/reviews/rev-1`);
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'alpha/one' })).toHaveAttribute('href', `#/a/${g.SLUG}/repos/alpha/one`);
  await expect(page.getByRole('link', { name: 'alpha/one#7', exact: true })).toHaveAttribute('href', `#/a/${g.SLUG}/pulls/alpha/one/7`);
  await expect(page.getByRole('link', { name: 'the review before' })).toHaveAttribute('href', `#/a/${g.SLUG}/reviews/older`);
});

test('an incomplete run with one open finding uses a singular count', async ({ page }) => {
  await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'failed' }, findings: [{ ...g.reviewDetail.findings[0], status: 'open' }] }], ...g.defaultApi()]);
  await page.goto(`${account}/reviews/rev-1`);
  await expect(page.getByLabel('Review outcome')).toContainText('1 open finding recorded');
});

test('a skipped run with a legacy error reason is not presented as an execution failure', async ({ page }) => {
  const skipped = { ...g.reviewDetail.review, status: 'skipped', skipReason: '', error: 'the pull request is closed' };
  await g.mockApi(page, [
    [/\/reviews\/rev-1$/, { ...g.reviewDetail, review: skipped, findings: [], summary: null }],
    [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [skipped], job: null }],
    ...g.defaultApi(),
  ]);
  await page.goto(`${account}/reviews/rev-1`);
  await expect(page.getByLabel('Review outcome')).toContainText('No review performed. the pull request is closed');
  await expect(page.getByText(/Execution problem|Showing the last review/)).toHaveCount(0);
  await page.goto(pullURL);
  await expect(page.getByText('Not reviewed yet.')).toBeVisible();
  await expect(page.getByText('Latest attempt skipped: the pull request is closed')).toHaveCount(1);
  await expect(page.getByText(/Showing the last review/)).toHaveCount(0);
});

test('inspector loads the next page and recovers from a failed append', async ({ page }) => {
  let fail = true;
  await page.route('**/findings?*', (route) => {
    const more = new URL(route.request().url()).searchParams.has('cursor');
    return route.fulfill({ status: more && fail ? 503 : 200, contentType: 'application/json', body: JSON.stringify(more && fail ? { message: 'Try again' } : more ? g.pageOf(findings.slice(1)) : g.pageOf(findings.slice(0, 1), 'next')) });
  });
  await page.goto(`${account}/findings`);
  await page.locator('.finding-link').click();
  const panel = page.getByRole('dialog');
  await panel.getByRole('button', { name: 'Next' }).click();
  await expect(panel.getByRole('alert')).toContainText('Could not load more findings');
  await expect(panel).toContainText('Finding 1');
  fail = false;
  await panel.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(panel).toContainText('Finding 2');
  await panel.getByRole('button', { name: 'Next' }).click();
  await expect(panel).toContainText('Finding 3');
  await expect(panel.getByRole('button', { name: 'Next' })).toBeDisabled();
  await page.keyboard.press('Escape');
  await expect(page.locator('.finding-link').nth(2)).toBeFocused();
  await expect(page.locator('.finding-row')).toHaveCount(3);
});

test('closing the inspector during pagination does not reopen it when the page arrives', async ({ page }) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/findings?*', async (route) => {
    const more = new URL(route.request().url()).searchParams.has('cursor');
    if (more) await pending;
    return route.fulfill({ json: more ? g.pageOf(findings.slice(1)) : g.pageOf(findings.slice(0, 1), 'next') });
  });
  await page.goto(`${account}/findings`);
  await page.locator('.finding-link').click();
  const panel = page.getByRole('dialog');
  await page.keyboard.press('j');
  await expect(panel.getByRole('button', { name: 'Next' })).toBeDisabled();
  await page.keyboard.press('Escape');
  release();
  await expect(page.locator('.finding-row')).toHaveCount(3);
  await expect(panel).toHaveCount(0);
  await expect(page.locator('.finding-link').first()).toBeFocused();
});

test('slash reveals and focuses file search while the navigator is hidden', async ({ page }) => {
  await page.goto(`${pullURL}?tab=diff`);
  await page.getByRole('button', { name: 'Hide navigator' }).click();
  await page.keyboard.press('/');
  await expect(page.getByRole('navigation', { name: 'Findings and files' })).toBeVisible();
  await expect(page.getByRole('searchbox', { name: 'Search files' })).toBeFocused();
});

test('activity selects a run within the PR workspace without refetching pull details', async ({ page }) => {
  let reads = 0;
  await page.route('**/pulls/alpha/one/7', (route) => {
    reads++;
    return route.fulfill({ json: { ...g.pullDetail, job: null } });
  });
  await page.goto(`${pullURL}?tab=activity`);
  const history = page.locator('.timeline-link').first();
  await expect(history).toHaveAttribute('href', `#/a/${g.SLUG}/pulls/alpha/one/7?run=rev-1`);
  const before = reads;
  await history.click();
  await expect(page.getByRole('region', { name: 'Take', exact: true })).toBeVisible();
  expect(reads).toBe(before);
  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Review history' })).toBeVisible();
});
