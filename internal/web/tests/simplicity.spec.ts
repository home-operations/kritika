import { test, expect } from './fixtures';
import * as g from './golden';
import AxeBuilder from '@axe-core/playwright';

const account = `/#/a/${g.SLUG}`;

for (const width of [390, 1440]) {
  test(`every page stays within the viewport at ${width}px`, async ({ page }, testInfo) => {
    await g.mockApi(page, [[/\/api\/v1\/me$/, { ...g.me, settings: { ...g.me.settings, theme: 'dark' } }], [/\/audit$/, g.pageOf([g.auditEvent])], ...g.defaultApi()]);
    await page.setViewportSize({ width, height: 900 });
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(e.message));
    const pages = [
      ['overview', '/#/'], ['all-pulls', '/#/pulls'], ['all-queue', '/#/queue'],
      ['account', account], ['analytics', `${account}/analytics`],
      ['usage', `${account}/usage`], ['repositories', `${account}/repos`],
      ['repository', `${account}/repos/alpha/one`], ['pulls', `${account}/pulls`],
      ['queue', `${account}/queue`], ['findings', `${account}/findings`],
      ['followups', `${account}/followups`], ['rules', `${account}/rules`],
      ['audit', `${account}/admin/audit`], ['settings', '/#/settings'], ['instance', '/#/admin'],
      ...['summary', 'diff', 'activity', 'timeline', 'conversation', 'usage', 'raw'].map((tab) =>
        [`review-${tab}`, `${account}/pulls/alpha/one/7?tab=${tab}`]),
    ];
    for (const [name, path] of pages) {
      await page.goto(path!);
      await expect(page.locator('main h1')).toBeVisible();
      await expect(page.locator('main [aria-busy="true"]')).toHaveCount(0);
      await expect(page.locator('main .state-msg[aria-live]')).toHaveCount(0);
      await expect(page.locator('main').getByRole('alert')).toHaveCount(0);
      await expect(page.getByText('Loading chart…', { exact: true })).toHaveCount(0);
      await page.evaluate(() => document.fonts.ready);
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), name).toBe(0);
      expect(await page.locator('main').evaluate((el) => el.scrollWidth - el.clientWidth), name).toBeLessThanOrEqual(1);
      if (width === 1440) await page.screenshot({ path: testInfo.outputPath(`${name}-desktop-dark.png`) });
    }
    expect(errors).toEqual([]);
  });
}

test('repository details stay accessible without displacing pull requests', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto(`${account}/repos/alpha/one`);
  await expect(page.getByRole('region', { name: 'Pull requests', exact: true })).toBeVisible();
  const settings = page.getByRole('button', { name: 'Repository settings', exact: true });
  await expect(settings).toHaveAttribute('aria-expanded', 'false');
  await settings.focus();
  await page.keyboard.press('Enter');
  await page.getByRole('searchbox', { name: 'Filter settings' }).fill('delta');
  await expect(page.locator('#repo-settings').locator('../..').getByRole('term')).toHaveText(['Max delta files']);
  await settings.click();
  await settings.click();
  await expect(page.getByRole('searchbox', { name: 'Filter settings' })).toHaveValue('delta');
  await page.getByRole('button', { name: 'Index runs', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Index runs', exact: true })).toBeVisible();
});

test('cache details can be shown without changing usage totals', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto(`${account}/usage`);
  const totals = page.getByRole('region', { name: 'Totals' });
  await expect(totals).toBeVisible();
  const before = await totals.innerText();
  await expect(page.getByRole('columnheader', { name: 'Cache read', exact: true })).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Show cache details' }).check();
  await expect(page.getByRole('columnheader', { name: 'Cache read', exact: true })).toBeVisible();
  expect(await totals.innerText()).toBe(before);
  await page.getByRole('checkbox', { name: 'Show cache details' }).uncheck();
  expect(await totals.innerText()).toBe(before);
});

test('mobile findings show status and open the inspector without sideways scrolling', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto(`${account}/findings`);
  const row = page.locator('.finding-row').first();
  await expect(row.locator('.status-word')).toBeInViewport();
  expect(await page.locator('.table-wrap:has(.finding-table)').evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await row.locator('.finding-link').click();
  await expect(page.getByRole('dialog', { name: 'Finding details' })).toBeVisible();
});

for (const [name, path] of [
  ['analytics', `${account}/analytics`],
  ['review', `${account}/pulls/alpha/one/7`],
  ['usage', `${account}/usage`],
  ['instance', '/#/admin'],
]) {
  test(`${name} buttons and button-like links never underline on hover`, async ({ page }) => {
    await g.mockApi(page, [[/\/audit$/, g.pageOf([g.auditEvent])], ...g.defaultApi()]);
    await page.goto(path!);
    await expect(page.locator('main h1')).toBeVisible();
    await expect(page.locator('main .state-msg[aria-live]')).toHaveCount(0);
    if (name === 'analytics') await page.getByRole('combobox', { name: 'Trend metric' }).selectOption('findings');
    const controls = page.locator('button, [role="button"], summary, a.btn, a.badge, a.sev-chip, a.pill, a.section-tab, a.tab, a.stat');
    let checked = 0;
    for (const control of await controls.all()) {
      if (!(await control.isVisible()) || !(await control.isEnabled())) continue;
      await control.hover();
      expect(await control.evaluate((el) => [el, ...el.querySelectorAll('*')].some((node) => getComputedStyle(node).textDecorationLine.includes('underline'))), await control.innerText()).toBe(false);
      checked++;
    }
    expect(checked).toBeGreaterThan(5);
  });
}
