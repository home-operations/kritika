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
  return g.mockApi(page, [
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
      (url: URL) => g.pageOf([
        { ...g.pull, title: "Handle worker cancellation safely" },
        {
          ...g.pull,
          number: 8,
          url: `${g.pull.url}-8`,
          title: "Add readiness checks",
          lastReview: { ...g.pull.lastReview!, status: "running" },
        },
        { ...g.pull, number: 9, url: `${g.pull.url}-9`, title: "Refresh controller dependencies" },
      ].filter((_, i) => !url.searchParams.has("limit") || url.searchParams.get("limit") !== "3" || i === (url.searchParams.get("outcome") === "failed" ? 0 : url.searchParams.get("is") === "p0" ? 1 : 2))),
    ],
    ...g.defaultApi(),
  ]);
}

test("overview attention links retain account scope and expose near limits", async ({ page }) => {
  const seen = await mockWorkspace(page);
  await page.goto("/");
  const attention = page.getByRole("region", { name: "Needs attention", exact: true });
  await expect(attention.getByRole("link")).toHaveCount(4);
  expect(seen.some((url) => url.pathname.endsWith('/pulls'))).toBe(false);
  await expect(attention.getByRole("link").first()).toHaveAttribute(
    "href",
    `#/a/${g.SLUG}/pulls?outcome=failed`,
  );
  await expect(attention.getByRole("link", { name: /Near limit/ })).toHaveAttribute("href", `#/a/${g.SLUG}/usage`);
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
  await page.getByRole("radio", { name: "Since last review", exact: true }).click();
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
  await page.getByRole("link", { name: "Include the job ID in retry logs: Inspect internal/worker/retry.go:2 in code" }).click();
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
    await expect(page.locator(".review-tab > .state-msg")).toContainText(
      swept ? "retention window" : "No diff recorded",
    );
    await expect(page.getByText("No files match these filters.")).toHaveCount(0);
    await expect(page.getByRole("searchbox", { name: "Search files" })).toHaveCount(0);
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
  await page.locator(".page-head h1").click();
  await expect(page.locator("main")).not.toBeFocused();
  await expect(page.locator("main")).not.toHaveAttribute("tabindex");
  await page.reload();
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
    await expect(page.locator(".overview-attention .attention-item")).toHaveCount(4);
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

test("review tabs remember selection, scope, filters, expansion and scroll", async ({ page }) => {
  await mockWorkspace(page);
  await page.setViewportSize({ width: 1280, height: 650 });
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff?finding=shutdown`);
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await page.getByRole("button", { name: "Next finding" }).click();
  await page.getByRole("radio", { name: "Since last review", exact: true }).click();
  await page.getByRole("checkbox", { name: "Files with findings" }).check();
  await page.getByRole("searchbox", { name: "Search files" }).fill("worker");
  const worker = page.locator(".diff-file").filter({ hasText: "internal/worker/worker.go" });
  await worker.locator(".diff-file-head button").click();
  await page.locator("main").evaluate((el) => { el.scrollTop = 300; });
  const scroll = await page.locator("main").evaluate((el) => el.scrollTop);
  // Activate without scrolling the tab into view first, as browser history does.
  await page.getByRole("navigation", { name: "Review sections" }).getByRole("link", { name: "Overview", exact: true }).evaluate((el: HTMLAnchorElement) => el.click());
  await expect(page.locator(".finding-nav")).toBeHidden();
  await page.keyboard.press("j");
  await page.goBack();
  await expect(page.locator(".finding-jump.active")).toContainText("Include the job ID");
  await expect(page.getByRole("radio", { name: "Since last review", exact: true })).toBeChecked();
  await expect(page.getByRole("searchbox", { name: "Search files" })).toHaveValue("worker");
  await expect(page.getByRole("checkbox", { name: "Files with findings" })).toBeChecked();
  await expect(worker.locator("table.diff")).toHaveCount(0);
  await expect.poll(() => page.locator("main").evaluate((el) => el.scrollTop)).toBe(scroll);
});

test("mobile navigation traps focus, closes on selection and restores focus on Escape", async ({ page }, testInfo) => {
  await mockWorkspace(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
  await page.getByRole("button", { name: "Next finding" }).click();
  const trigger = page.getByRole("button", { name: "Open navigation" });
  await trigger.click();
  const menu = page.getByRole("dialog", { name: "Navigation" });
  await expect(menu).toBeVisible();
  await page.keyboard.press("j");
  await expect(page.locator(".finding-jump.active")).toContainText("Drain queued jobs");
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press("Tab");
    expect(await menu.evaluate((el) => el.contains(document.activeElement))).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(menu).toBeVisible();
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: testInfo.outputPath("navigation-mobile.png") });
  await menu.getByRole("link", { name: "Pull requests", exact: true }).click();
  await expect(menu).toHaveCount(0);
  await expect(page).toHaveURL(/\/pulls$/);
});

for (const theme of ["light", "dark"] as const) {
  test(`large reviews remain navigable in ${theme}`, async ({ page }, testInfo) => {
    await mockWorkspace(page);
    await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1536, height: 1100 });
    const extraFiles = Array.from({ length: 300 }, (_, i) =>
      `diff --git a/generated/file-${i}.go b/generated/file-${i}.go\n--- a/generated/file-${i}.go\n+++ b/generated/file-${i}.go\n@@ -1,20 +1,20 @@\n${" unchanged\n".repeat(20)}`,
    ).join("");
    await page.route("**/reviews/rev-1/diff", (route) => route.fulfill({ json: { diff: workerDiff + retryDiff + extraFiles, deltaDiff: workerDiff } }));
    await page.route("**/reviews/rev-1", (route) => route.fulfill({ json: {
      ...review, findings: review.findings.map((f, i) => i ? f : { ...f, explanation: `${f.explanation}\n\n${"A long explanation should wrap inside the finding without widening the page. ".repeat(100)}\n\n${"unbroken_identifier_".repeat(30)}` }),
    } }));
    await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
    await expect(page.locator(".file-jump")).toHaveCount(302);
    await expect(page.locator("table.diff")).toHaveCount(0);
    await page.getByRole("searchbox", { name: "Search files" }).fill("file-299");
    await expect(page.locator(".file-jump")).toHaveCount(1);
    await page.getByRole("checkbox", { name: "Files with findings" }).check();
    await expect(page.getByText("No files match these filters.")).toBeVisible();
    await page.getByRole("searchbox", { name: "Search files" }).fill("");
    await expect(page.locator(".file-jump")).toHaveCount(2);
    await page.getByRole("button", { name: "Next finding" }).click();
    await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
    await expect(page.locator("table.diff")).toHaveCount(1);
    expect(await page.locator("main").evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
    await page.getByRole("button", { name: "Next finding" }).click();
    await expect(page.locator("#diff-finding-logging")).toBeFocused();
    await expect(page.getByRole("button", { name: "Next finding" })).toBeDisabled();
    await page.getByRole("button", { name: "Previous finding" }).click();
    await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
    await page.locator("main").evaluate((el) => { el.scrollTop = 260; });
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: testInfo.outputPath(`large-review-${theme}.png`) });
  });
}

test("overview alerts remain usable without fetching pull previews", async ({ page }) => {
  await mockWorkspace(page);
  await page.route("**/pulls?**outcome=failed", (route) => route.fulfill({ status: 503, json: { message: "Temporarily unavailable" } }));
  await page.goto("/");
  const attention = page.getByRole("region", { name: "Needs attention", exact: true });
  await expect(attention.getByRole("link", { name: /1 failed/ })).toHaveAttribute("href", `#/a/${g.SLUG}/pulls?outcome=failed`);
  await expect(attention.getByRole("alert")).toHaveCount(0);
  await attention.getByRole("link", { name: /1 failed/ }).click();
  await expect(page.getByRole("alert")).toContainText('Temporarily unavailable');
  await page.unroute("**/pulls?**outcome=failed");
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.locator('.pull-row')).toHaveCount(3);
});

test.describe("desktop 200% zoom equivalent reflow", () => {
  test.use({ viewport: { width: 768, height: 550 }, deviceScaleFactor: 2 });
  test("review controls and menu fit half the CSS viewport", async ({ page }, testInfo) => {
    await mockWorkspace(page);
    await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
    await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
    await page.getByRole("searchbox", { name: "Search files" }).fill("retry");
    await expect(page.locator(".file-jump")).toHaveCount(1);
    expect(await page.locator("main").evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
    await page.locator("main").evaluate((el) => { el.scrollTop = 220; });
    await page.screenshot({ path: testInfo.outputPath("review-200-percent.png") });
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`workspace text contrast and keyboard focus in ${theme}`, async ({ page }) => {
    await mockWorkspace(page);
    await page.emulateMedia({ colorScheme: theme });
    for (const route of ["/", `/#/a/${g.SLUG}/reviews/rev-1/diff`]) {
      await page.goto(route);
      await expect(page.locator(".page-head h1")).toBeVisible();
      if (route === "/") await expect(page.locator(".overview-attention .attention-item")).toHaveCount(4);
      else await expect(page.locator("table.diff")).toHaveCount(2);
      const failures = await page.locator(".page-head h1, .overview-attention .pill, .overview-attention strong, .overview-attention .muted, .finding-jump strong, .finding-jump .muted, .finding-jump .sev, .finding-controls .btn, .file-filters label, .section-tab.active").evaluateAll((elements) => {
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 1;
        const ctx = canvas.getContext("2d", { willReadFrequently: true })!;
        const luminance = (rgb: Uint8ClampedArray) => {
          const linear = Array.from(rgb).slice(0, 3).map((c) => {
            const n = c / 255;
            return n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4;
          });
          return linear[0]! * 0.2126 + linear[1]! * 0.7152 + linear[2]! * 0.0722;
        };
        return elements.flatMap((el) => {
          if (!(el instanceof HTMLElement) || !el.getClientRects().length || el.matches(":disabled")) return [];
          const ancestors: Element[] = [];
          for (let parent: Element | null = el; parent; parent = parent.parentElement) ancestors.unshift(parent);
          ctx.fillStyle = "white";
          ctx.fillRect(0, 0, 1, 1);
          for (const ancestor of ancestors) {
            ctx.fillStyle = getComputedStyle(ancestor).backgroundColor;
            ctx.fillRect(0, 0, 1, 1);
          }
          const bg = luminance(ctx.getImageData(0, 0, 1, 1).data);
          ctx.fillStyle = getComputedStyle(el).color;
          ctx.fillRect(0, 0, 1, 1);
          const fg = luminance(ctx.getImageData(0, 0, 1, 1).data);
          const ratio = (Math.max(bg, fg) + 0.05) / (Math.min(bg, fg) + 0.05);
          return ratio >= 4.5 ? [] : [{ text: el.textContent?.trim(), ratio }];
        });
      });
      expect(failures).toEqual([]);
    }
    await page.getByRole("button", { name: "Next finding" }).focus();
    await page.keyboard.press("Enter");
    await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
    const outline = await page.locator("#diff-finding-shutdown").evaluate((el) => getComputedStyle(el).outlineWidth);
    expect(parseFloat(outline)).toBeGreaterThanOrEqual(2);
    await page.keyboard.press("/");
    await expect(page.getByRole("searchbox", { name: "Search files" })).toBeFocused();
  });
}

test("summary links can revisit a finding after selecting a different one", async ({ page }) => {
  await mockWorkspace(page);
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff?finding=shutdown`);
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await page.getByRole("button", { name: "Next finding" }).click();
  await expect(page).toHaveURL(/finding=logging$/);
  await page.getByRole("navigation", { name: "Review sections" }).getByRole("link", { name: "Overview", exact: true }).click();
  await page.getByRole("link", { name: "Drain queued jobs before exiting: Inspect internal/worker/worker.go:4 in code" }).click();
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await expect(page).toHaveURL(/finding=shutdown$/);
});

const typeChangeDiff = `diff --git a/internal/worker/worker.go b/internal/worker/worker.go
deleted file mode 100644
--- a/internal/worker/worker.go
+++ /dev/null
@@ -1 +0,0 @@
-old content
diff --git a/internal/worker/worker.go b/internal/worker/worker.go
new file mode 120000
--- /dev/null
+++ b/internal/worker/worker.go
@@ -0,0 +1 @@
+target.go
`;

test("type-change diff entries have unique targets and anchor each finding once", async ({ page }) => {
  await mockWorkspace(page);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/reviews/rev-1", (route) => route.fulfill({ json: { ...review, findings: [{ ...review.findings[0]!, line: 1, endLine: 1 }] } }));
  await page.route("**/reviews/rev-1/diff", (route) => route.fulfill({ json: { diff: typeChangeDiff } }));
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
  await expect(page.locator(".file-jump")).toHaveCount(2);
  expect(errors).toEqual([]);
  await expect(page.locator("#diff-finding-shutdown")).toHaveCount(1);
  const ids = await page.locator(".diff-file").evaluateAll((els) => els.map((el) => el.id));
  expect(new Set(ids).size).toBe(2);
  await page.getByRole("button", { name: "Next finding" }).click();
  await expect(page.locator(".diff-file").last().locator("#diff-finding-shutdown")).toBeFocused();
  for (let i = 0; i < 2; i++) {
    await page.locator(".file-jump").nth(i).click();
    await expect(page.locator(".diff-file").nth(i)).toBeFocused();
  }
});

test("explicit summary links refocus the already-selected finding after scrolling away", async ({ page }) => {
  await mockWorkspace(page);
  await page.setViewportSize({ width: 1280, height: 650 });
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff?finding=shutdown`);
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await page.locator("main").evaluate((el) => { el.scrollTop = 0; });
  await page.getByRole("navigation", { name: "Review sections" }).getByRole("link", { name: "Overview", exact: true }).click();
  await page.getByRole("link", { name: "Drain queued jobs before exiting: Inspect internal/worker/worker.go:4 in code" }).click();
  await expect(page.locator("#diff-finding-shutdown")).toBeFocused();
  await page.getByRole("navigation", { name: "Review sections" }).getByRole("link", { name: "Overview", exact: true }).click();
  await page.getByRole("link", { name: "Findings & code", exact: true }).click();
  await expect(page).toHaveURL(/finding=shutdown$/);
});

test("code selection does not draw a file focus ring, keyboard file navigation does", async ({ page }) => {
  await mockWorkspace(page);
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
  const file = page.locator(".diff-file").first();
  await file.locator(".code").first().click();
  expect(await file.evaluate((el) => getComputedStyle(el).outlineStyle)).toBe("none");
  await expect(page.locator("main")).not.toBeFocused();
  await page.locator(".file-jump").first().focus();
  await page.keyboard.press("Enter");
  await expect(file).toBeFocused();
  expect(await file.evaluate((el) => getComputedStyle(el).outlineStyle)).toBe("solid");
});

for (const width of [390, 768, 1536]) {
  test(`admin select-all remains usable at ${width}px`, async ({ page }) => {
    await mockWorkspace(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`/#/a/${g.SLUG}/pulls`);
    const all = page.locator("thead").getByRole("checkbox", { name: "Select every pull request shown" });
    await all.check();
    await expect(page.getByRole("group", { name: "Selected pull requests" })).toContainText("3 selected");
    await all.uncheck();
    await expect(page.getByRole("group", { name: "Selected pull requests" })).toHaveCount(0);
  });
}

test("sidebar tab order and current-page markers match visible navigation", async ({ page }) => {
  await mockWorkspace(page);
  await page.goto(`/#/a/${g.SLUG}`);
  const nav = page.locator(".desktop-navigation nav.sections");
  await expect(nav.locator('[aria-current="page"]')).toHaveCount(1);
  await expect(nav.getByRole("link", { name: "Reviews", exact: true })).toHaveCount(0);
  await page.locator(".brand").focus();
  await page.keyboard.press("Tab");
  await expect(nav.getByRole("link", { name: "Overview", exact: false })).toBeFocused();
  await nav.getByRole("link", { name: "Findings", exact: true }).click();
  await expect(nav.locator('[aria-current="page"]')).toHaveCount(1);
  await expect(nav.locator('[aria-current="page"]')).toHaveText("Findings");
  await expect(nav).toContainText("Configuration");
});

test("summary and code use the same list formatting for multiline merge impact", async ({ page }) => {
  await mockWorkspace(page);
  await page.route("**/reviews/rev-1", (route) => route.fulfill({ json: { ...review, summary: { ...review.summary, impact: ["First concern\ncontinued here", "Second concern"] } } }));
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1`);
  const summary = await page.locator('[aria-labelledby="sum-impact"] .md').innerHTML();
  await page.getByRole("link", { name: "Findings & code", exact: true }).click();
  await page.getByRole("region", { name: "Review summary" }).getByRole("button").click();
  await expect(page.locator(".merge-impact .md")).toHaveJSProperty("innerHTML", summary);
});

test("file-only navigation does not reopen stale targets after changing diff scope", async ({ page }) => {
  await mockWorkspace(page);
  const diff = `diff --git a/large.go b/large.go\n--- a/large.go\n+++ b/large.go\n@@ -1,600 +1,600 @@\n${" unchanged\n".repeat(600)}`;
  await page.route("**/reviews/rev-1/diff", (route) => route.fulfill({ json: { diff, deltaDiff: diff } }));
  await page.goto(`/#/a/${g.SLUG}/reviews/rev-1/diff`);
  await page.getByRole("button", { name: "Open large.go", exact: true }).click();
  await expect(page.locator("table.diff")).toHaveCount(1);
  await page.getByRole("radio", { name: "Since last review", exact: true }).click();
  await expect(page.locator("table.diff")).toHaveCount(0);
});
