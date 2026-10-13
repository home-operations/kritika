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
    expect(Math.abs(full.height / 2 - half.height)).toBeLessThan(0.1);
    expect(Math.abs(full.y + full.height - half.y - half.height)).toBeLessThan(0.1);
    expect(half.x).toBeGreaterThan(full.x);

    await columns.nth(1).locator('path, rect').first().hover();
    await expect(panel.locator('.chart-tip')).toContainText('Sep 2');
    await chart.focus();
    await chart.press('Home');
    await expect(panel.locator('[aria-live="polite"]')).toContainText('Sep 1');
    await chart.press('End');
    await expect(panel.locator('[aria-live="polite"]')).toContainText('Sep 3');
    await expect.poll(async () => {
      const tip = (await panel.locator('.chart-tip').boundingBox())!;
      const bounds = (await chart.boundingBox())!;
      return tip.x >= bounds.x - 1 && tip.x + tip.width <= bounds.x + bounds.width + 1;
    }).toBe(true);
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

test('sparse stacks share a baseline and only separate visible neighbors', async ({ page }) => {
  const values = [
    { p0: 1, p1: 2, p2: 3 },
    { p0: 2, p1: 4, p2: 0 },
    { p0: 0, p1: 6, p2: 0 },
    { p0: 2, p1: 0, p2: 4 },
  ];
  const series = values.map((findings, i) => ({ ...g.analytics.series[0]!, key: `2026-09-0${i + 1}`, findings }));
  await g.mockApi(page, [[/\/analytics$/, { ...g.analytics, series }], ...g.defaultApi()]);
  await page.goto(`/#/a/${g.SLUG}/analytics`);
  const columns = page.getByRole('region', { name: 'Findings by severity' }).locator('.chart-col');
  await expect(columns).toHaveCount(4);
  const reference = (await columns.first().boundingBox())!;
  for (const column of await columns.all()) {
    const box = (await column.boundingBox())!;
    expect(Math.abs(box.y - reference.y)).toBeLessThan(0.1);
    expect(Math.abs(box.height - reference.height)).toBeLessThan(0.1);
    const bars = await column.locator('path, rect').all();
    for (let i = 1; i < bars.length; i++) {
      const below = (await bars[i - 1]!.boundingBox())!;
      const above = (await bars[i]!.boundingBox())!;
      expect(below.y - above.y - above.height).toBeCloseTo(2);
    }
  }
});
