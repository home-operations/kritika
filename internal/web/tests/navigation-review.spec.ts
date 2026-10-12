import { test, expect } from './fixtures';
import * as g from './golden';
import type { Repository, Job } from '../src/lib/types';

const account = `/#/a/${g.SLUG}`;
const repos = (count: number): Repository[] => Array.from({ length: count }, (_, i) => ({ ...g.repoPage.items[0]!, id: `repo-${i}`, fullName: `alpha/repo-${i}` }));

// Keep reconnects out of request-count and delayed-response assertions.
test.beforeEach(async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.addInitScript(() => {
    window.EventSource = class extends EventTarget {
      constructor() { super(); queueMicrotask(() => this.dispatchEvent(new Event('open'))); }
      close() {}
    } as unknown as typeof EventSource;
  });
});

test('a short remembered list can grow on Back and a fresh sidebar visit starts at one page', async ({ page }) => {
  let count = 3;
  const limits: number[] = [];
  await page.route('**/repos?*', (r) => {
    const q = new URL(r.request().url()).searchParams;
    const start = Number(q.get('cursor') ?? 0);
    const limit = Number(q.get('limit') ?? 50);
    limits.push(limit);
    const end = Math.min(start + limit, count);
    return r.fulfill({ json: g.pageOf(repos(count).slice(start, end), end < count ? String(end) : null) });
  });
  const nav = page.locator('.desktop-navigation');
  await page.goto(`${account}/repos`);
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(3);
  await nav.getByRole('link', { name: 'Queue', exact: true }).click();
  count = 8;
  await page.goBack();
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(8);
  expect(limits.at(-1)).toBe(100);
  await nav.getByRole('link', { name: 'Queue', exact: true }).click();
  count = 250;
  await nav.getByRole('link', { name: 'Repositories', exact: true }).click();
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(200);
  await page.locator('.repo-table tbody tr').last().scrollIntoViewIfNeeded();
  await nav.getByRole('link', { name: 'Queue', exact: true }).click();
  await nav.getByRole('link', { name: 'Repositories', exact: true }).click();
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(100);
  expect(await page.locator('main.page').evaluate((el) => el.scrollTop)).toBe(0);
});

test('editing a repository search preserves scroll and its history entry', async ({ page }) => {
  await page.route('**/repos?*', (r) => r.fulfill({ json: g.pageOf(repos(100)) }));
  await page.goto(`${account}/repos`);
  await expect(page.locator('.repo-table tbody tr')).toHaveCount(100);
  await page.locator('main.page').evaluate((el) => { el.scrollTop = 500; });
  const entry = await page.evaluate(() => history.state.kritikaEntry);
  for (const q of ['r', 're', 'repo']) {
    await page.getByPlaceholder('Filter by name').evaluate((el: HTMLInputElement, value) => {
      el.value = value;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, q);
    await expect(page.getByPlaceholder('Filter by name')).toHaveValue(q);
    await expect.poll(() => page.locator('main.page').evaluate((el) => el.scrollTop)).toBe(500);
    expect(await page.evaluate(() => history.state.kritikaEntry)).toBe(entry);
  }
});

test('a shorter restored queue settles before later polls expand it', async ({ page }) => {
  await page.clock.install();
  let count = 100;
  await page.route('**/queue', (r) => r.fulfill({ json: Array.from({ length: count }, (_, i) => ({ ...g.job, id: i + 1 })) }));
  await page.goto(`${account}/queue`);
  await expect(page.locator('.queue-table tbody tr')).toHaveCount(100);
  await page.locator('.queue-table tbody tr').last().scrollIntoViewIfNeeded();
  await page.locator('.desktop-navigation').getByRole('link', { name: 'Repositories', exact: true }).click();
  count = 12;
  await page.goBack();
  await expect(page.locator('.queue-table tbody tr')).toHaveCount(12);
  const shorterTop = await page.locator('main.page').evaluate((el) => el.scrollTop);
  count = 100;
  await page.clock.runFor(15_001);
  await expect(page.locator('.queue-table tbody tr')).toHaveCount(100);
  await expect.poll(() => page.locator('main.page').evaluate((el) => el.scrollTop)).toBe(shorterTop);
});

for (const failure of [false, true]) {
  test(`restored deep lists show the first page while the next request is pending: failure=${failure}`, async ({ page }) => {
    let returning = false;
    let release!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/repos?*', async (r) => {
      const q = new URL(r.request().url()).searchParams;
      const start = Number(q.get('cursor') ?? 0);
      if (returning && start === 200) {
        await held;
        if (failure) return r.fulfill({ status: 500, json: { message: 'Later page failed' } });
      }
      const end = Math.min(start + Number(q.get('limit') ?? 100), 450);
      return r.fulfill({ json: g.pageOf(repos(450).slice(start, end), end < 450 ? String(end) : null) });
    });
    await page.goto(`${account}/repos`);
    for (const count of [200, 300, 400, 450]) {
      await page.getByRole('button', { name: 'Load more' }).click();
      await expect(page.locator('.repo-table tbody tr')).toHaveCount(count);
    }
    await page.locator('.repo-table tbody tr').last().scrollIntoViewIfNeeded();
    await page.locator('.desktop-navigation').getByRole('link', { name: 'Queue', exact: true }).click();
    returning = true;
    await page.goBack();
    await expect(page.locator('.repo-table tbody tr')).toHaveCount(200);
    release();
    if (failure) {
      await expect(page.getByRole('alert')).toContainText('Later page failed');
      await expect(page.locator('.repo-table tbody tr')).toHaveCount(200);
    } else await expect(page.locator('.repo-table tbody tr')).toHaveCount(450);
  });
}

test('navigating away from a repository type clears its bulk selection', async ({ page }) => {
  await page.route('**/repos?*', (r) => {
    const fork = new URL(r.request().url()).searchParams.get('type') === 'forks';
    return r.fulfill({ json: g.pageOf([{ ...g.repoPage.items[0]!, fullName: fork ? 'alpha/fork' : 'alpha/main', fork }]) });
  });
  await page.goto(`${account}/repos?type=forks`);
  await page.getByRole('checkbox', { name: 'Select alpha/fork' }).check();
  await expect(page.getByRole('group', { name: 'Selected repositories' })).toBeVisible();
  await page.locator('.desktop-navigation').getByRole('link', { name: 'Repositories', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: 'Select alpha/main' })).not.toBeChecked();
  await expect(page.getByRole('group', { name: 'Selected repositories' })).toHaveCount(0);
});

test('completed jobs retain historical errors only inside details', async ({ page }) => {
  const job: Job = { ...g.job, state: 'completed', finalizedAt: new Date().toISOString(), lastError: 'Historical network failure' };
  await page.route('**/queue', (r) => r.fulfill({ json: [job] }));
  await page.goto(`${account}/queue`);
  await expect(page.locator('.queue-table')).toContainText('completed');
  await expect(page.locator('.job-error-summary')).toHaveCount(0);
  await page.getByLabel(`Details for job #${job.id}`).click();
  await expect(page.locator('.error-text')).toHaveText(job.lastError);
});

test('reporting breadcrumb keeps the period and 24-hour analytics explains partial days', async ({ page }) => {
  await page.goto(`${account}/usage?days=90`);
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: g.SLUG }).click();
  await expect(page).toHaveURL(/\?days=90$/);
  await page.getByRole('radio', { name: '24 hours' }).click();
  await expect(page.getByText('Last 24 hours, split by date.', { exact: false })).toBeVisible();
  await expect(page.getByRole('img', { name: /^Failure rate per partial day/ })).toBeVisible();
});

for (const [word, label] of [['spend', 'Usage & costs'], ['configuration', 'Instance settings']]) {
  test(`palette preserves the ${word} search synonym`, async ({ page }) => {
    await page.goto(`${account}/queue`);
    await page.keyboard.press('Control+k');
    await page.locator('.palette-input input').fill(word!);
    await expect(page.locator('.palette-row').filter({ hasText: label })).toBeVisible();
  });
}
