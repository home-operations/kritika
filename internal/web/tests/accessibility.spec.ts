import AxeBuilder from '@axe-core/playwright';
import { test, expect } from './fixtures';
import * as g from './golden';

for (const theme of ['light', 'dark'] as const) {
  test.describe(`${theme} accessibility`, () => {
    test.use({ colorScheme: theme });

    test.beforeEach(async ({ page }) => {
      await g.mockApi(page, g.defaultApi());
    });

    test('token suggestions', async ({ page }) => {
      await page.goto(`/#/a/${g.SLUG}/pulls`);
      const search = page.getByRole('combobox', { name: 'Search pull requests' });
      await search.fill('status:');
      await expect(page.getByRole('listbox', { name: 'Suggestions' })).toBeVisible();
      await expect(page.locator('html')).toHaveClass(new RegExp(theme));
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    });

    test('chart and table views', async ({ page }) => {
      await page.goto(`/#/a/${g.SLUG}/analytics`);
      const trends = page.getByRole('region', { name: 'Reviews', exact: true });
      await expect(trends.getByRole('img')).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      await trends.getByRole('radio', { name: 'Table', exact: true }).click();
      await expect(trends.getByRole('table')).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    });

    test('command dialog', async ({ page }) => {
      await page.goto(`/#/a/${g.SLUG}/pulls`);
      await expect(page.getByRole('heading', { name: 'Pull requests', exact: true })).toBeVisible();
      await page.keyboard.press('Control+k');
      await expect(page.getByRole('dialog')).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    });
  });
}
