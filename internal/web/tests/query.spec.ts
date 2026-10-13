import { test, expect } from './fixtures';
import * as g from './golden';

const account = `#/a/${g.SLUG}`;

test.beforeEach(async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.route('**/api/events', (route) => route.fulfill({ contentType: 'text/event-stream', body: ': connected\n\n' }));
});

test('the setup banner and checklist share a single request', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto('/#/admin');
  await expect(page.locator('#op-setup')).toBeVisible();
  expect(seen.filter((u) => u.pathname === '/api/v1/admin/setup')).toHaveLength(1);
});

test('returning to a fresh page reuses its cached result', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(`/${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  await page.evaluate((hash) => { location.hash = hash; }, `${account}/queue`);
  await expect(page.locator('table.data')).toBeVisible();
  await page.evaluate((hash) => { location.hash = hash; }, `${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  expect(seen.filter((u) => u.pathname.endsWith('/rules'))).toHaveLength(1);
});

test('a live event invalidates an inactive cached page', async ({ page }) => {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/events', async (route) => {
    await gate;
    await route.fulfill({ contentType: 'text/event-stream', body: `event: review\ndata: ${JSON.stringify({ ...g.liveEvent, account: g.SLUG })}\n\n` });
  });
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(`/${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  await page.evaluate((hash) => { location.hash = hash; }, `${account}/queue`);
  await expect(page.locator('table.data')).toBeVisible();
  release();
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/queue')).length).toBeGreaterThan(1);
  await page.evaluate((hash) => { location.hash = hash; }, `${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/rules')).length).toBe(2);
});

test('a successful write invalidates cached pages', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await g.mockWrites(page, [['PUT', /\/api\/v1\/me\/settings$/, { status: 204 }]]);
  await page.goto(`/${account}/rules`);
  await expect(page.locator('main')).toContainText(g.rule.text);
  await page.evaluate(() => { location.hash = '#/settings'; });
  const saved = page.waitForResponse((response) => response.url().endsWith('/me/settings') && response.request().method() === 'PUT');
  await page.getByRole('radio', { name: '12-hour', exact: true }).click();
  await saved;
  await page.evaluate((hash) => { location.hash = hash; }, `${account}/rules`);
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/rules')).length).toBe(2);
});

for (const end of ['sign out', 'session expiry'] as const) {
  test(`${end} clears data before another user signs in`, async ({ page, mockProviders }) => {
    let anotherUser = false;
    let ruleReads = 0;
    await g.mockApi(page, [
      [/\/rules$/, () => { ruleReads++; return [{ ...g.rule, text: anotherUser ? 'Second user rule' : 'First user rule' }]; }],
      ...g.defaultApi(),
    ]);
    await mockProviders([{ name: 'local', type: 'local', displayName: 'Admin' }]);
    await page.route('**/auth/logout', (route) => route.fulfill({ status: 204 }));
    await page.route('**/auth/local', (route) => {
      anotherUser = true;
      return route.fulfill({ status: 204 });
    });
    await page.goto(`/${account}/rules`);
    await expect(page.locator('main')).toContainText('First user rule');
    if (end === 'sign out') {
      await page.locator('.user-button').click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    } else {
      await page.route('**/queue', (route) => route.fulfill({ status: 401, json: { code: 'unauthenticated' } }));
      await page.evaluate((hash) => { location.hash = hash; }, `${account}/queue`);
    }
    await expect(page).toHaveURL(/#\/signin$/);
    await page.unroute('**/queue');
    const form = page.getByRole('form', { name: 'Admin sign-in' });
    await form.getByLabel('Username').fill('second');
    await form.getByLabel('Password').fill('password');
    await form.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.locator('.signin-card')).toHaveCount(0);
    await page.evaluate((hash) => { location.hash = hash; }, `${account}/rules`);
    await expect(page.locator('main')).toContainText('Second user rule');
    await expect(page.locator('main')).not.toContainText('First user rule');
    expect(ruleReads).toBe(2);
  });
}

test('a rolling usage window advances when its cached query refetches', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-12T12:00:00Z') });
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/events', async (route) => {
    await gate;
    await route.fulfill({ contentType: 'text/event-stream', body: `event: model_call\ndata: ${JSON.stringify({ kind: 'model_call', account: g.SLUG })}\n\n` });
  });
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(`/${account}/usage`);
  await page.getByRole('radio', { name: '24 hours', exact: true }).click();
  const windows = () => seen.filter((u) => u.pathname.endsWith('/usage') && u.searchParams.get('from')?.includes('T'));
  await expect.poll(() => windows().length).toBeGreaterThan(0);
  const before = Date.parse(windows().at(-1)!.searchParams.get('from')!);
  await page.clock.fastForward(60_000);
  const event = page.waitForResponse((response) => response.url().endsWith('/api/events'));
  release();
  await event;
  await page.clock.runFor(2100);
  await expect.poll(() => Date.parse(windows().at(-1)!.searchParams.get('from')!)).toBeGreaterThanOrEqual(before + 60_000);
});

test('leaving a page aborts its unfinished query', async ({ page }) => {
  let requested = false;
  let cancelled = false;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  page.on('requestfailed', (request) => {
    if (request.url().endsWith('/rules')) cancelled = true;
  });
  await page.route('**/rules', async (route) => {
    requested = true;
    await gate;
    await route.fulfill({ json: [g.rule] });
  });
  await page.goto(`/${account}/rules`);
  await expect.poll(() => requested).toBe(true);
  await page.evaluate((hash) => { location.hash = hash; }, `${account}/queue`);
  await expect.poll(() => cancelled).toBe(true);
  release();
  await expect(page.locator('table.data')).toBeVisible();
});
