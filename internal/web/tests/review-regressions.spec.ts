import { readFileSync } from 'node:fs';
import { test, expect } from './fixtures';
import * as g from './golden';

for (const text of ['st', 'status:failed ']) {
  test(`Enter preserves unselected token search: ${JSON.stringify(text)}`, async ({ page }) => {
    await g.mockApi(page, g.defaultApi());
    await page.goto(`/#/a/${g.SLUG}/pulls`);
    const input = page.getByRole('combobox', { name: 'Search pull requests' });
    await input.fill(text);
    await expect(page.getByRole('listbox')).toBeVisible();
    await input.press('Enter');
    await expect(input).toHaveValue(text);
    await input.press('ArrowDown');
    await input.press('Enter');
    await expect(input).not.toHaveValue(text);
  });
}

for (const picker of ['tokens', 'zones']) {
  test(`${picker} keep native input editing keys`, async ({ page }) => {
    await g.mockApi(page, g.defaultApi());
    await page.goto(picker === 'tokens' ? `/#/a/${g.SLUG}/pulls` : '/#/settings');
    const input = page.getByRole('combobox', { name: picker === 'tokens' ? 'Search pull requests' : 'Time zone', exact: true });
    const value = picker === 'tokens' ? 'status:failed ' : 'America';
    await input.fill(value);
    await expect(page.getByRole('listbox')).toBeVisible();
    await input.press('Home');
    expect(await input.evaluate((el: HTMLInputElement) => el.selectionStart)).toBe(0);
    await input.press('End');
    expect(await input.evaluate((el: HTMLInputElement) => el.selectionStart)).toBe(value.length);
    for (const key of ['PageUp', 'PageDown']) {
      expect(await input.evaluate((el, key) => el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })), key)).toBe(true);
    }
  });
}

test('page content clicks preserve keyboard scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 360 });
  await g.mockApi(page, g.defaultApi());
  await page.goto(`/#/a/${g.SLUG}/analytics`);
  await page.getByRole('heading', { name: 'Analytics', exact: true }).click();
  await page.keyboard.press('PageDown');
  await page.keyboard.press('ArrowDown');
  await expect.poll(() => page.locator('main').evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
});

for (const route of ['/queue', `/a/${g.SLUG}/queue`]) {
  test(`queue caption aligns its help control: ${route}`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await g.mockApi(page, g.defaultApi());
    await page.goto(`/#${route}`);
    const help = page.getByRole('button', { name: 'About the queue', exact: true });
    await expect(help).toBeVisible();
    const caption = help.locator('..');
    await expect(caption).toHaveCSS('display', 'flex');
    const bounds = (await caption.boundingBox())!;
    const button = (await help.boundingBox())!;
    expect(Math.abs(bounds.x + bounds.width - button.x - button.width)).toBeLessThan(1);
  });
}

test('desktop live status is not clipped', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto('/#/settings');
  const status = page.locator('.topbar .live .sr-only');
  await expect(status).toHaveText('Live updates on');
  await expect(status).toHaveCSS('clip-path', 'none');
});

test('command palette retains its specific radius and shadow', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto('/#/settings');
  await page.getByRole('heading', { name: 'Your settings' }).waitFor();
  await page.keyboard.press('Control+k');
  const dialog = page.getByRole('dialog');
  await expect(dialog).toHaveCSS('border-radius', '10px');
  expect(await dialog.evaluate((el) => getComputedStyle(el).boxShadow)).toContain('0px 16px 48px');
});

test('exact metric hints are available over the number', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto(`/#/a/${g.SLUG}/usage`);
  const tile = page.locator('.stat').filter({ hasText: 'API spend in selection' });
  expect(await tile.getByRole('button').getAttribute('aria-label')).toContain(await tile.locator('.stat-value').innerText());
  await tile.locator('.stat-value').hover();
  await expect(page.getByRole('tooltip')).toBeVisible();
  await expect(page.getByRole('tooltip')).toHaveText(/; \d[\d,]* tokens$/);
});

test('chart code loads only when a chart is shown', async ({ page }) => {
  const manifest = JSON.parse(readFileSync(new URL('../dist/.vite/manifest.json', import.meta.url), 'utf8')) as Record<string, { file: string }>;
  const chartFile = manifest['src/lib/components/ColumnChartContent.svelte']!.file;
  const requests: string[] = [];
  page.on('request', (request) => requests.push(new URL(request.url()).pathname));
  await g.mockApi(page, g.defaultApi());
  await page.goto('/#/settings');
  await expect(page.getByRole('heading', { name: 'Your settings' })).toBeVisible();
  expect(requests.some((path) => path.endsWith(chartFile))).toBe(false);
  await page.goto(`/#/a/${g.SLUG}/analytics`);
  await expect(page.getByRole('img', { name: /^Completed reviews per day/ })).toBeVisible();
  expect(requests.some((path) => path.endsWith(chartFile))).toBe(true);
});
