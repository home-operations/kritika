import { test, expect } from "./fixtures";
import * as g from "./golden";
import { href } from "../src/lib/routes";
import type { ReviewDetail } from "../src/lib/types";

const review: ReviewDetail = {
  ...g.reviewDetail,
  review: {
    ...g.reviewDetail.review,
    pull: { ...g.reviewDetail.review.pull, title: "Handle worker cancellation safely" },
    status: "completed",
    confidence: null,
  },
  summary: {
    ...g.reviewDetail.summary!,
    headline: "Drain pending jobs before the worker exits",
    take: "Cancellation can leave accepted work unfinished. The shutdown path should account for jobs still in the queue.",
    impact: ["Queued jobs may be lost if the worker exits before draining them."],
  },
  findings: [
    {
      ...g.reviewDetail.findings[0]!,
      id: "shutdown",
      path: "internal/worker/worker.go",
      line: 4,
      endLine: 4,
      title: "Drain queued jobs before exiting",
      explanation:
        "Returning here skips the pending queue. Jobs accepted just before cancellation can be lost.",
      suggestedFix: "Drain accepted work before returning from the worker.",
      replacement: "",
      agentPrompt: "",
      rules: [],
      severity: "p0",
    },
    {
      ...g.reviewDetail.findings[0]!,
      id: "logging",
      path: "internal/worker/retry.go",
      line: 2,
      endLine: 2,
      title: "Include the job ID in retry logs",
      explanation: "A job identifier makes retries easier to trace.",
      suggestedFix: "",
      replacement: "",
      agentPrompt: "",
      rules: [],
      severity: "p2",
    },
  ],
};
const workerDiff =
  "diff --git a/internal/worker/worker.go b/internal/worker/worker.go\n--- a/internal/worker/worker.go\n+++ b/internal/worker/worker.go\n@@ -1,5 +1,6 @@\n func run(ctx context.Context) error {\n     select {\n     case <-ctx.Done():\n+        return ctx.Err()\n     }\n }\n";
const retryDiff =
  'diff --git a/internal/worker/retry.go b/internal/worker/retry.go\n--- a/internal/worker/retry.go\n+++ b/internal/worker/retry.go\n@@ -1,2 +1,3 @@\n func retry() {\n+    slog.Info("Retrying job")\n }\n';

async function mockWorkspace(page: Parameters<typeof g.mockApi>[0]) {
  await g.mockApi(page, [
    [/\/reviews\/rev-1$/, review],
    [
      /\/reviews\/rev-1\/diff$/,
      { diff: workerDiff + retryDiff, deltaDiff: workerDiff, swept: false },
    ],
    [
      /\/api\/v1\/accounts$/,
      [
        {
          ...g.accountSummary,
          attention: { failed: 1, capped: 0, p0: 2, paused: 1 },
          usage: { ...g.accountSummary.usage, reviewsToday: 46, reviewsPerDay: 50 },
        },
      ],
    ],
    [
      /\/api\/v1\/queue$/,
      { ...g.instanceQueue, jobs: [{ ...g.instanceQueue.jobs[0]!, state: "running" }] },
    ],
    [
      /\/pulls$/,
      g.pageOf([
        { ...g.pull, title: "Handle worker cancellation safely" },
        {
          ...g.pull,
          number: 8,
          url: `${g.pull.url}-8`,
          title: "Add readiness checks",
          lastReview: { ...g.pull.lastReview!, status: "running" },
        },
        { ...g.pull, number: 9, url: `${g.pull.url}-9`, title: "Refresh controller dependencies" },
      ]),
    ],
    ...g.defaultApi(),
  ]);
}

test("overview attention links retain account scope and expose near limits", async ({ page }) => {
  await mockWorkspace(page);
  await page.goto("/");
  const attention = page.getByRole("region", { name: "Needs attention", exact: true });
  await expect(attention.getByRole("link")).toHaveCount(4);
  await expect(attention.getByRole("link").first()).toHaveAttribute(
    "href",
    `#/a/${g.SLUG}/pulls?outcome=failed`,
  );
  await expect(attention.getByRole("link").last()).toHaveAttribute("href", `#/a/${g.SLUG}/usage`);
  await expect(attention).toContainText("46 of today's 50 reviews");
  await attention.getByRole("link").first().click();
  await expect(page).toHaveURL(/pulls\?outcome=failed$/);
});

test("findings open collapsed files, support keyboard navigation, and survive a diff scope change", async ({
  page,
}) => {
  await mockWorkspace(page);
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
  const worker = page.locator(".diff-file").filter({ hasText: "internal/worker/worker.go" });
  await worker.locator(".diff-file-head button").click();
  await expect(worker.locator("table.diff")).toHaveCount(0);
  await page.getByRole("button", { name: /p0 Drain queued jobs/ }).click();
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await expect(worker.locator("table.diff")).toBeVisible();
  await page.keyboard.press("j");
  await expect(page.locator("#diff-finding-logging")).toBeFocused();
  await page.getByRole("button", { name: "Since last review", exact: true }).click();
  await expect(page.getByRole("region", { name: "Findings outside this diff" })).toContainText(
    "Include the job ID",
  );
  await expect(page.locator("#diff-finding-logging")).toBeFocused();
  await page.keyboard.press("k");
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await page.keyboard.press("?");
  await page.keyboard.press("j");
  await expect(page.locator(".finding-jump.active")).toContainText("Drain queued jobs");
});

test("summary links open the matching finding in code and work after reload", async ({ page }) => {
  await mockWorkspace(page);
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1`);
  await page.getByRole("link", { name: "Inspect internal/worker/retry.go:2 in code" }).click();
  await expect(page).toHaveURL(/\/diff\?finding=logging$/);
  await expect(page.locator("#diff-finding-logging")).toBeFocused();
  await page.reload();
  await expect(page.locator("#diff-finding-logging")).toBeFocused();
});

for (const swept of [false, true]) {
  test(`findings remain readable without a recorded diff: swept=${swept}`, async ({ page }) => {
    await mockWorkspace(page);
    await page.route("**/reviews/rev-1/diff", (route) =>
      route.fulfill({ json: { diff: "", deltaDiff: "", swept } }),
    );
    await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
    await expect(page.locator(".state-msg")).toContainText(
      swept ? "retention window" : "No diff recorded",
    );
    await page.getByRole("button", { name: /p2 Include/ }).click();
    await expect(page.locator("#diff-finding-logging")).toBeFocused();
  });
}

for (const width of [390, 768, 1536]) {
  test(`workspace navigation and content fit at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1024 });
    await mockWorkspace(page);
    for (const route of [
      { name: "overview" },
      { name: "pulls", slug: g.SLUG },
      { name: "review", slug: g.SLUG, id: "rev-1", tab: "diff" },
    ] as const) {
      await page.goto(`/${href(route)}`);
      await expect(page.locator(".page-head h1")).toBeVisible();
      await expect(page.locator(".state-error")).toHaveCount(0);
      expect(
        await page.locator("main").evaluate((el) => el.scrollWidth - el.clientWidth),
      ).toBeLessThanOrEqual(1);
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
      if (route.name === "review") {
        const nav = await page.locator(".finding-nav").boundingBox();
        const code = await page.locator(".review-code").boundingBox();
        expect(
          nav && code && (width > 800 ? nav.x + nav.width <= code.x : nav.y + nav.height <= code.y),
        ).toBe(true);
      }
    }
  });
}

test("skip link focuses the page content", async ({ page }) => {
  await mockWorkspace(page);
  await page.goto("/");
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to content" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("main")).toBeFocused();
});

for (const theme of ["light", "dark"] as const) {
  test(`workspace screenshots in ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1536, height: 1100 });
    await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
    await mockWorkspace(page);
    await page.goto("/");
    await expect(page.getByRole("region", { name: "Needs attention", exact: true })).toBeVisible();
    await expect(page.locator(".recent-pulls tbody tr")).toHaveCount(3);
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: testInfo.outputPath(`overview-${theme}.png`) });
    await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
    await expect(page.locator(".review-code table.diff")).toHaveCount(2);
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: testInfo.outputPath(`review-${theme}.png`) });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/#/a/${g.SLUG}/pulls`);
    await expect(page.locator(".pull-table tbody tr")).toHaveCount(3);
    await page.screenshot({ path: testInfo.outputPath(`pulls-mobile-${theme}.png`) });
  });
}
