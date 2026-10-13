import AxeBuilder from '@axe-core/playwright';
import { test, expect } from './fixtures';
import * as g from './golden';

test.beforeEach(async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
});

test('toolbar hints open on focus and dismiss without losing the trigger', async ({ page }) => {
  await page.goto('/#/settings');
  const button = page.getByRole('button', { name: 'Keyboard shortcuts', exact: true });
  await button.focus();
  await expect(page.getByRole('tooltip')).toHaveText('Keyboard shortcuts (?)');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('tooltip')).toHaveCount(0);
  await expect(button).toBeFocused();
  await button.press('Enter');
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('tooltip')).toHaveCount(0);
});

test('metric explanations are available to keyboard users', async ({ page }) => {
  await page.goto(`/#/a/${g.SLUG}/analytics`);
  const button = page.locator('.stat').getByRole('button', { name: /^About / }).first();
  await button.focus();
  const hint = page.getByRole('tooltip');
  await expect(hint).toBeVisible();
  await expect(button).toHaveAttribute('aria-describedby', (await hint.getAttribute('id'))!);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test('time zones filter by city, retain the selection on Escape, and save Auto', async ({ page }) => {
  const sent = await g.mockWrites(page, [['PUT', /\/api\/v1\/me\/settings$/, { status: 204 }]]);
  await page.goto('/#/settings');
  const input = page.getByRole('combobox', { name: 'Time zone', exact: true });
  await input.fill('New York');
  await expect(page.getByRole('option')).toHaveText(['America/New_York']);
  await input.press('Escape');
  await expect(input).toHaveValue('Auto');
  await expect(page.getByRole('listbox')).toHaveCount(0);
  expect(sent).toHaveLength(0);

  await input.fill('no such zone');
  await expect(page.getByRole('listbox').getByRole('status')).toHaveText('No matching time zones.');
  await input.press('Enter');
  expect(sent).toHaveLength(0);
  await input.fill('Tokyo');
  await input.press('ArrowDown');
  await input.press('Enter');
  await expect(input).toHaveValue('Asia/Tokyo');
  await expect.poll(() => sent.map((s) => s.body)).toEqual([{ timeZone: 'Asia/Tokyo', clock: '', theme: '' }]);
  await input.fill('auto');
  await page.getByRole('option', { name: 'Auto', exact: true }).click();
  await expect(input).toHaveValue('Auto');
  await expect.poll(() => sent.map((s) => s.body)).toEqual([
    { timeZone: 'Asia/Tokyo', clock: '', theme: '' },
    { timeZone: '', clock: '', theme: '' },
  ]);
});

test('a rejected time-zone change restores the saved selection', async ({ page }) => {
  await g.mockWrites(page, [['PUT', /\/api\/v1\/me\/settings$/, g.apiError(400, 'bad_request', 'time zone rejected')]]);
  await page.goto('/#/settings');
  const input = page.getByRole('combobox', { name: 'Time zone', exact: true });
  await input.fill('Tokyo');
  await page.getByRole('option', { name: 'Asia/Tokyo', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Not saved');
  await expect(input).toHaveValue('Auto');
});
