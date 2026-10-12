import { test, expect } from './fixtures';
import * as g from './golden';

for (const width of [1280, 390]) {
  test(`stacked columns retain their values and readouts at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const series = [
      { ...g.analytics.series[0]!, key: '2026-09-01', findings: { p0: 0, p1: 0, p2: 0 } },
      { ...g.analytics.series[0]!, key: '2026-09-02', findings: { p0: 1, p1: 2, p2: 3 } },
      { ...g.analytics.series[0]!, key: '2026-09-03', findings: { p0: 0, p1: 3, p2: 0 } },
    ];
    await g.mockApi(page, [[/\/analytics$/, { ...g.analytics, series }], ...g.defaultApi()]);
    await page.goto(`/#/a/${g.SLUG}/analytics`);
    const panel = page.getByRole('region', { name: 'Findings by severity' });
    const chart = panel.getByRole('img');
    const columns = chart.locator('.chart-col');
    await expect(columns).toHaveCount(3);
    await expect(columns.nth(0).locator('path, rect')).toHaveCount(0);
    await expect(columns.nth(1).locator('path, rect')).toHaveCount(3);
    await expect(columns.nth(2).locator('path, rect')).toHaveCount(1);
    const full = (await columns.nth(1).boundingBox())!;
    const half = (await columns.nth(2).boundingBox())!;
    expect(full.width).toBeGreaterThan(0);
    expect(full.height).toBeGreaterThan(100);
    expect(Math.abs(full.height / 2 - half.height)).toBeLessThanOrEqual(2);
    expect(half.x).toBeGreaterThan(full.x);

    await columns.nth(1).locator('path, rect').first().hover();
    await expect(panel.locator('.chart-tip')).toContainText('Sep 2');
    await chart.focus();
    await chart.press('Home');
    await expect(panel.locator('[aria-live="polite"]')).toContainText('Sep 1');
    await chart.press('End');
    await expect(panel.locator('[aria-live="polite"]')).toContainText('Sep 3');
    await chart.press('Escape');
    await expect(panel.locator('.chart-tip')).toHaveCount(0);
    await panel.getByRole('radio', { name: 'Table', exact: true }).click();
    const rows = panel.locator('tbody tr');
    await expect(rows).toHaveCount(3);
    await expect(rows.nth(0).locator('td.num')).toHaveText(['0', '3', '0', '3']);
    await expect(rows.nth(1).locator('td.num')).toHaveText(['1', '2', '3', '6']);
    await expect(rows.nth(2).locator('td.num')).toHaveText(['0', '0', '0', '0']);
    await panel.getByRole('radio', { name: 'Chart', exact: true }).click();
    await expect(columns.nth(1).locator('path, rect')).toHaveCount(3);
  });
}

test('zero-valued columns have a finite axis and no bars', async ({ page }) => {
  await g.mockApi(page, [[/\/analytics$/, { ...g.analytics, series: [{ ...g.analytics.series[0]!, reviews: 0 }] }], ...g.defaultApi()]);
  await page.goto(`/#/a/${g.SLUG}/analytics`);
  const chart = page.getByRole('img', { name: /^Completed reviews per day/ });
  await expect(chart.locator('.chart-tick').filter({ hasText: /^\d+$/ })).toHaveText(['0', '1', '2']);
  await expect(chart.locator('.chart-col path, .chart-col rect')).toHaveCount(0);
  await chart.focus();
  await chart.press('Home');
  await expect(page.locator('.chart-tip')).toContainText(/0\s+Reviews/);
});
