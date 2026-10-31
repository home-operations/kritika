import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? 'html' : 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    // --host 127.0.0.1 pins the preview server to IPv4 loopback. With no
    // --host, vite preview binds the bare string "localhost", which Node can
    // resolve IPv6-first (::1). A v4-only 127.0.0.1 client (Playwright's own
    // webServer readiness probe, and baseURL above) then times out with
    // connection refused even though the server is up. If 4173 is busy,
    // --strictPort fails fast instead of silently picking another port.
    // The command calls Vite by name and finds it on the PATH that pnpm or npx
    // sets up. The Playwright image that ui-test-container runs this in has no
    // pnpm.
    command: 'vite build && vite preview --port 4173 --host 127.0.0.1 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
  },
});
