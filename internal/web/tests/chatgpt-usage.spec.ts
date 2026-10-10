import { test, expect } from "./fixtures";
import * as g from "./golden";
import type { ChatGPTProviderUsage } from "../src/lib/types";

const account = `#/a/${g.SLUG}`;
const enabled = [/\/api\/v1\/accounts$/, [{ ...g.accountSummary, chatgptEnabled: true }]] as [
  RegExp,
  unknown,
];

test("ChatGPT controls are hidden when no plan provider is configured", async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(`/${account}/usage`);
  await expect(page.getByRole("heading", { name: "API spend by day" })).toBeVisible();
  await expect(page.getByRole("region", { name: "ChatGPT allowances" })).toHaveCount(0);
  await expect(page.getByRole("radiogroup", { name: "Billing", exact: true })).toHaveCount(0);
  expect(seen.some((u) => u.pathname.endsWith("/chatgpt/allowances"))).toBe(false);
});

for (const scenario of [
  { name: "not connected", connected: false, message: "ChatGPT is not connected." },
  { name: "quota unavailable", connected: true, message: "Allowance data unavailable." },
]) {
  test(`ChatGPT allowances: ${scenario.name}`, async ({ page }) => {
    await g.mockApi(page, [
      enabled,
      [
        /\/chatgpt\/allowances$/,
        [{ provider: "plan", connected: scenario.connected, allowances: [] }],
      ],
      ...g.defaultApi(),
    ]);
    await page.goto(`/${account}/usage`);
    await expect(page.getByRole("region", { name: "ChatGPT allowances" })).toContainText(
      scenario.message,
    );
    await expect(page.getByRole("meter", { name: /allowance used/ })).toHaveCount(0);
  });
}

test("quota windows retain their durations and show stale resets honestly", async ({ page }) => {
  const now = Math.floor(Date.now() / 1000);
  const providers: ChatGPTProviderUsage[] = [
    {
      provider: "plan",
      connected: true,
      allowances: [
        {
          limitId: "codex",
          observedAt: new Date().toISOString(),
          primary: { usedPercent: 25, windowDurationMins: 300, resetsAt: now + 3600 },
          secondary: { usedPercent: 90, windowDurationMins: 10080, resetsAt: now - 3600 },
        },
        {
          limitId: "codex_other",
          observedAt: new Date().toISOString(),
          primary: { usedPercent: 0, windowDurationMins: 60, resetsAt: 9e18 },
          secondary: null,
        },
      ],
    },
  ];
  await g.mockApi(page, [enabled, [/\/chatgpt\/allowances$/, providers], ...g.defaultApi()]);
  await page.goto(`/${account}/usage`);
  const panel = page.getByRole("region", { name: "ChatGPT allowances" });
  await expect(panel).toContainText("75% remaining");
  await expect(panel).toContainText("10% remaining");
  await expect(panel).toContainText("Reset time passed; awaiting updated usage");
  await expect(panel).toContainText("Reset time unavailable");
  await expect(panel.getByRole("meter", { name: "5-hour allowance used" })).toHaveAttribute(
    "aria-valuenow",
    "25",
  );
  await expect(panel.getByRole("meter", { name: "Weekly allowance used" })).toHaveAttribute(
    "aria-valuenow",
    "90",
  );
  await expect(panel.getByRole("meter", { name: "1-hour allowance used" })).toHaveAttribute(
    "aria-valuenow",
    "0",
  );
});

test("ChatGPT filtering supports hour and week and counts cached input once", async ({ page }) => {
  const row = {
    ...g.usageSeries.rows[0]!,
    inputTokens: 1000,
    cacheReadTokens: 800,
    cacheWriteTokens: 100,
    outputTokens: 200,
    costUsd: 0,
    calls: 2,
    planCalls: 2,
  };
  const seen = await g.mockApi(page, [
    enabled,
    [/\/chatgpt\/allowances$/, [{ provider: "plan", connected: true, allowances: [] }]],
    [
      /\/usage$/,
      (u: URL) => {
        const group = u.searchParams.get("group") ?? "day";
        return {
          ...g.usageSeries,
          group,
          rows: [{ ...row, key: group === "hour" ? "2026-01-05T03:00:00Z" : "2026-01-05" }],
        };
      },
    ],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${account}/usage`);
  await page.getByRole("radio", { name: "ChatGPT plan", exact: true }).click();
  const summary = page.getByRole("region", { name: "ChatGPT usage in this period" });
  await expect(summary).toBeVisible();
  await expect(summary.locator('[title="1,200"]')).toBeVisible();
  await page.getByRole("radio", { name: "Hour", exact: true }).click();
  await expect(page.getByRole("img", { name: "Tokens by hour, last 24 hours" })).toBeVisible();
  await expect
    .poll(() =>
      seen.some(
        (u) =>
          u.searchParams.get("group") === "hour" && u.searchParams.get("billing") === "chatgpt",
      ),
    )
    .toBe(true);
  const hourly = seen.find((u) => u.searchParams.get("group") === "hour")!;
  const since = Date.now() - Date.parse(hourly.searchParams.get("from")!);
  expect(since).toBeGreaterThan(23.9 * 3_600_000);
  expect(since).toBeLessThan(24.1 * 3_600_000);
  await page.getByRole("radio", { name: "Week", exact: true }).click();
  await expect(page.getByRole("img", { name: "Tokens by week, last 24 hours" })).toBeVisible();
  await expect(page.locator("tbody")).toContainText("Week of");
  await expect
    .poll(() =>
      seen.some(
        (u) =>
          u.searchParams.get("group") === "week" && u.searchParams.get("billing") === "chatgpt",
      ),
    )
    .toBe(true);
});

test("review plan usage is totalled per run and excludes API fallback tokens", async ({ page }) => {
  const plan = {
    ...g.reviewDetail.usage[0]!,
    model: "plan/gpt-6-sol",
    costUsd: 0,
    chatgptPlan: true,
    inputTokens: 1000,
    outputTokens: 200,
  };
  const usage = [
    { ...plan, runnerRunId: "11111111-1111-1111-1111-111111111111" },
    { ...plan, runnerRunId: "11111111-1111-1111-1111-111111111111" },
    { ...plan, runnerRunId: "22222222-2222-2222-2222-222222222222" },
    { ...plan },
    { ...plan, model: "api/fallback", costUsd: 0.25, chatgptPlan: false, inputTokens: 9999 },
  ];
  await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, usage }], ...g.defaultApi()]);
  await page.goto(`/${account}/reviews/rev-1/usage`);
  const summary = page.getByRole("region", { name: "ChatGPT usage for this review" });
  await expect(summary.locator('[title="4,000"]')).toBeVisible();
  const runs = page.getByRole("region", { name: "ChatGPT usage by run" });
  await expect(runs.locator("tbody tr")).toHaveCount(3);
  const first = runs.getByRole("row").filter({ hasText: "Run 11111111" });
  await expect(first.getByRole("cell").nth(1)).toHaveText("2");
  await expect(first.getByRole("cell").nth(4)).toHaveAttribute("title", "2,400");
  await expect(runs).toContainText("Run not recorded");
  await expect(page.locator("tfoot")).toContainText("$0.25");
});
