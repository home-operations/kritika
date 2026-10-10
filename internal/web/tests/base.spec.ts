import type { Page } from '@playwright/test';
import { test, expect, DEFAULT_ME } from './fixtures';
import { builtEntry, resyncFrame } from './golden';

// The server mounts the UI, API and auth routes under KRITIKA_WEB_URL's path.
// The preview server only serves the root, so the prefix is added here by
// proxying /kritika/<asset> to /<asset>.
async function servePrefixed(page: Page): Promise<void> {
  await page.route(/\/kritika\/(index\.html)?$|\/kritika\/(assets\/|favicon)/, async (route) => {
    const u = new URL(route.request().url());
    u.pathname = u.pathname.replace(/^\/kritika/, '') || '/';
    await route.fulfill({ response: await route.fetch({ url: u.toString() }) });
  });
  await page.route('**/kritika/api/v1/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DEFAULT_ME) }),
  );
}

test('served under a path prefix, every request stays under it', async ({ page }) => {
  const paths: string[] = [];
  page.on('request', (r) => paths.push(new URL(r.url()).pathname));
  await servePrefixed(page);
  await page.goto('/kritika/index.html');
  await expect(page.locator('.sections .section-tab').first()).toHaveAttribute('href', '#/');
  expect(paths).toContain('/kritika/api/v1/me');
  expect(paths).toContain('/kritika/api/events');
  expect(paths.filter((p) => !p.startsWith('/kritika/'))).toEqual([]);
});

// The resync's entry resolves against the prefix as the page's own script
// did, so the page that booted from that script stays.
test('served under a path prefix, a stream from the build the page runs keeps it', async ({ page }) => {
  await servePrefixed(page);
  let opens = 0;
  await page.route('**/kritika/api/events', (route) => {
    opens++;
    return route.fulfill({ status: 200, contentType: 'text/event-stream', body: resyncFrame(builtEntry()) });
  });
  let loads = 0;
  page.on('request', (r) => {
    if (r.isNavigationRequest()) loads++;
  });
  await page.goto('/kritika/index.html');
  // A stream reopens only once the one before it ended, its resync handled.
  await expect.poll(() => opens).toBeGreaterThan(1);
  expect(loads).toBe(1);
});
