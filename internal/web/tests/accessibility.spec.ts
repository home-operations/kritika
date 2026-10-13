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
      const trends = page.getByRole('region', { name: 'Review trends', exact: true });
      await expect(trends.getByRole('img')).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      await trends.getByRole('radio', { name: 'Table', exact: true }).click();
      await expect(trends.getByRole('table')).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    });

    test('searchable time zones', async ({ page }) => {
      await page.goto('/#/settings');
      await page.getByRole('combobox', { name: 'Time zone', exact: true }).fill('America');
      await expect(page.getByRole('listbox', { name: 'Time zones' })).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    });

    test('destructive confirmation', async ({ page }) => {
      await g.mockApi(page, [
        [/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'running' } }],
        ...g.defaultApi(),
      ]);
      await page.goto(`/#/a/${g.SLUG}/reviews/rev-1`);
      await page.getByRole('button', { name: 'Cancel review', exact: true }).click();
      await expect(page.getByRole('alertdialog')).toBeVisible();
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
