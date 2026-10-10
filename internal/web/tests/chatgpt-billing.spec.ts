import { test, expect } from "./fixtures";
import * as g from "./golden";

const account = `#/a/${g.SLUG}`;
const plan = { ...g.reviewDetail.usage[0]!, model: "plan/reviewer", costUsd: 0, chatgptPlan: true };
const paid = { ...plan, model: "api/fallback", costUsd: 0.25, chatgptPlan: false };
const free = { ...paid, model: "api/free", costUsd: 0 };
const unpriced = { ...free, model: "anthropic/new-model", unpriced: true };

for (const scenario of [
  { name: "plan only", usage: [plan], planCalls: 1, unpricedCalls: 0, cost: 0, spend: "Included in plan" },
  { name: "plan and paid fallback", usage: [plan, paid], planCalls: 1, unpricedCalls: 0, cost: 0.25, spend: "$0.25" },
  { name: "free API", usage: [free], planCalls: 0, unpricedCalls: 0, cost: 0, spend: "$0" },
  { name: "unpriced API", usage: [unpriced], planCalls: 0, unpricedCalls: 1, cost: 0, spend: "Unpriced" },
  { name: "paid and unpriced", usage: [paid, unpriced], planCalls: 0, unpricedCalls: 1, cost: 0.25, spend: "$0.25 + unpriced" },
]) {
  test(`review billing: ${scenario.name}`, async ({ page }) => {
    const review = {
      ...g.reviewDetail.review,
      costUsd: scenario.cost,
      calls: scenario.usage.length,
      planCalls: scenario.planCalls,
      unpricedCalls: scenario.unpricedCalls,
    };
    await g.mockApi(page, [
      [/\/reviews\/rev-1$/, { ...g.reviewDetail, review, usage: scenario.usage }],
      [
        /\/reviews\/rev-1\/transcript$/,
        {
          ...g.transcript,
          turns: scenario.usage.map((u, i) => ({
            ...g.transcript.turns[0]!,
            id: `call-${i}`,
            index: i,
            step: i,
            model: u.model,
            costUsd: u.costUsd,
            chatgptPlan: u.chatgptPlan,
            unpriced: "unpriced" in u && u.unpriced,
          })),
        },
      ],
      ...g.defaultApi(),
    ]);

    await page.goto(`/${account}/reviews/rev-1/usage`);
    const fact = page
      .locator(".facts > div")
      .filter({ has: page.getByText("API spend", { exact: true }) });
    await expect(fact).toContainText(scenario.spend);
    const rows = page.locator("tbody tr");
    for (const u of scenario.usage) {
      const row = rows.filter({ hasText: u.model });
      await expect(row).toContainText(u.chatgptPlan ? "ChatGPT plan" : "API");
      await expect(row).toContainText(
        "unpriced" in u && u.unpriced ? "Unpriced" : u.chatgptPlan ? "Included in plan" : u.costUsd ? "$0.25" : "$0",
      );
      await expect(row.getByRole("cell").nth(4)).toHaveAttribute("title", String(u.inputTokens));
    }
    await expect(page.locator("tfoot")).toContainText(scenario.unpricedCalls ? scenario.spend : scenario.cost ? "$0.25" : "$0");
    if (scenario.unpricedCalls) await expect(page.getByText(/API spend is incomplete/)).toBeVisible();
    await expect(rows.getByText("Included in plan", { exact: true })).toHaveCount(
      scenario.planCalls,
    );

    await page.goto(`/${account}/reviews/rev-1/conversation`);
    for (const u of scenario.usage) {
      const turn = page.locator(".turn-head").filter({ hasText: u.model });
      await expect(turn).toContainText(
        "unpriced" in u && u.unpriced ? "Unpriced" : u.chatgptPlan ? "Included in plan" : u.costUsd ? "$0.25" : "$0",
      );
      await expect(turn.getByText("ChatGPT plan", { exact: true })).toHaveCount(
        u.chatgptPlan ? 1 : 0,
      );
    }
  });
}

test("usage groups distinguish plan, mixed, free API and unpriced calls while totals count API spend", async ({
  page,
}) => {
  const base = g.usageSeries.rows[0]!;
  await g.mockApi(page, [
    [/\/api\/v1\/accounts$/, [{ ...g.accountSummary, chatgptEnabled: true }]],
    [/\/chatgpt\/allowances$/, [{ provider: "plan", connected: true, allowances: [] }]],
    [
      /\/usage$/,
      {
        ...g.usageSeries,
        group: "model",
        rows: [
          { ...base, key: "plan/reviewer", costUsd: 0, calls: 2, planCalls: 2 },
          { ...base, key: "mixed/reviewer", costUsd: 0.25, calls: 2, planCalls: 1 },
          { ...base, key: "api/free", costUsd: 0, calls: 1, planCalls: 0 },
          { ...base, key: "api/unpriced", costUsd: 0, calls: 1, unpricedCalls: 1 },
          { ...base, key: "api/incomplete", costUsd: 0.5, calls: 2, unpricedCalls: 1 },
        ],
      },
    ],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${account}/usage`);
  const rows = page.locator("tbody tr");
  await expect(rows.filter({ hasText: "plan/reviewer" })).toContainText("ChatGPT plan");
  await expect(rows.filter({ hasText: "plan/reviewer" })).toContainText("Included in plan");
  await expect(rows.filter({ hasText: "mixed/reviewer" })).toContainText("API + ChatGPT plan");
  await expect(rows.filter({ hasText: "mixed/reviewer" })).toContainText("$0.25");
  await expect(rows.filter({ hasText: "api/free" })).toContainText("$0");
  await expect(rows.filter({ hasText: "api/free" })).not.toContainText("Included in plan");
  await expect(page.locator("tfoot")).toContainText("Total API spend");
  await expect(rows.filter({ hasText: "api/unpriced" })).toContainText("Unpriced");
  await expect(rows.filter({ hasText: "api/incomplete" })).toContainText("$0.50 + unpriced");
  await expect(page.locator("tfoot")).toContainText("$0.75 + unpriced");
  await expect(page.getByText(/API spend is incomplete: 2 unpriced calls/)).toBeVisible();
  await expect(page.getByRole("link", { name: "Manage ChatGPT usage" })).toHaveAttribute(
    "href",
    "https://chatgpt.com/settings/usage",
  );
  await page.getByRole("radio", { name: "Tokens", exact: true }).click();
  await expect(page.getByRole("img", { name: /Tokens by model/ })).toBeVisible();
});

test("spend summaries mark incomplete totals and avoid comparing incomplete periods", async ({ page }) => {
  const usage = { ...g.accountSummary.usage, unpricedCalls: 1, reviewUnpricedCalls: 1 };
  await g.mockApi(page, [
    [/\/api\/v1\/accounts$/, [{ ...g.accountSummary, usage }]],
    [/\/admin\/accounts$/, [{ ...g.adminAccount, usage }]],
    [/\/analytics$/, { ...g.analytics, current: { ...g.analytics.current, unpricedCalls: 1 } }],
    [/\/reviews\/rev-1$/, { ...g.reviewDetail, agentRun: { ...g.reviewDetail.agentRun!, unpricedCalls: 1 } }],
    ...g.defaultApi(),
  ]);
  await page.goto('/');
  await expect(page.locator('.tile').filter({ hasText: 'API spend this month' })).toContainText('$1.50 + unpriced');
  await page.goto(`/${account}`);
  const spend = page.locator('.stat').filter({ has: page.getByText('API spend', { exact: true }) });
  await expect(spend).toContainText('$1.25 + unpriced');
  await expect(spend).toContainText('spend is incomplete');
  await expect(spend.locator('.delta')).toHaveCount(0);
  await page.goto(`/${account}/usage`);
  const month = page.locator('.stat').filter({ hasText: 'API spend this month' });
  await expect(month).toContainText('$1.50 + unpriced');
  await expect(month).toContainText('$0.08 + unpriced per review (median)');
  await page.goto('/#/admin');
  await expect(page.locator('tbody tr').filter({ hasText: g.adminAccount.slug }).first()).toContainText('$1.50 + unpriced');
  await page.goto(`/${account}/reviews/rev-1/timeline`);
  await expect(page.locator('.panel-head').filter({ hasText: 'Agent steps' })).toContainText('$0.10 + unpriced');
});
