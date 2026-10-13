import { test, expect } from './fixtures';
import * as g from './golden';
import type { AccountDetail, Pull, ReviewStatus, Rule } from '../src/lib/types';

const T = `#/a/${g.SLUG}`;

test.beforeEach(async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
});

test.describe('overview', () => {
  test('a single-account member stays on the breakdown instead of leaving for the account', async ({ page }) => {
    await page.goto('/#/');
    await expect(page.locator('.page-head h1')).toHaveText('Operations overview');
    await expect(page).toHaveURL(/#\/$/);
    const rows = page.locator('table.account-breakdown tbody tr');
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText(g.accountSummary.slug);
    await rows.first().getByRole('link', { name: g.accountSummary.slug }).click();
    await expect(page).toHaveURL(new RegExp(`#/a/${g.accountSummary.slug}$`));
  });

  test("each account's row says what wants a look and whether its webhooks arrive, and leads there", async ({ page }) => {
    const a = g.accountSummary;
    const quiet = { ...a, slug: 'github/quiet', attention: { failed: 0, capped: 0, p0: 0, paused: 0 } };
    const polled = { ...a, slug: 'github/polled', lastWebhookAt: null, usage: { ...a.usage, tokens: a.usage.tokensPerMonth } };
    await g.mockApi(page, [[/\/api\/v1\/accounts$/, [a, quiet, polled]], ...g.defaultApi()]);
    await page.goto('/#/');
    const row = (slug: string) => page.getByRole('row').filter({ hasText: slug });
    await expect(row(a.slug).locator('.account-wants .pill')).toHaveText([`${a.attention.failed} failed`, `${a.attention.p0} P0`]);
    await expect(row(a.slug).getByRole('link', { name: /P0/ })).toHaveAttribute('href', `#/a/${a.slug}/pulls?is=p0`);
    await expect(row(a.slug)).toContainText('receiving');
    await expect(row('github/quiet').locator('.account-wants')).toHaveText('—');
    await expect(row('github/polled')).toContainText('polling only');
    await expect(row('github/polled').locator(`time[datetime="${a.lastPolledAt}"]`)).toBeVisible();
    await expect(row('github/polled').getByRole('link', { name: 'cap' })).toHaveAttribute('href', '#/a/github/polled/usage');
  });

  test('several accounts each get a row, and the tiles add them up', async ({ page }) => {
    await g.mockApi(page, [[/\/api\/v1\/accounts$/, [g.accountSummary, { ...g.accountSummary, slug: 'beta' }]], ...g.defaultApi()]);
    await page.goto('/#/');
    await expect(page.locator('table.account-breakdown tbody tr')).toHaveCount(2);
    const tiles = page.getByRole('region', { name: 'Across all accounts' });
    await expect(tiles.locator('.stat').filter({ hasText: 'Reviews, last 7 days' })).toContainText(String(2 * g.accountSummary.reviews7d));
    const spend = tiles.locator('.stat').filter({ hasText: 'API spend this month' });
    await expect(spend.locator('.stat-value')).toHaveText('$3.00');
    // The per-review figure is a mean over both accounts' reviews, not a sum of their medians.
    await expect(spend).toContainText('$0.10 per review, mean of 24');
    const attention = page.getByRole('region', { name: 'Needs attention', exact: true });
    await expect(attention.getByRole('link', { name: /1 failed/ })).toHaveCount(2);
  });

  test('a tile says what runs and waits now and how busy the model slots are, and opens the queue', async ({ page }) => {
    const j = g.instanceQueue.jobs[0]!;
    const q = { jobs: [{ ...j, id: 1, state: 'running' }, { ...j, id: 2, state: 'available' }, j, { ...j, id: 3, state: 'discarded' }], slots: [...g.instanceQueue.slots, { ...g.instanceQueue.slots[0]!, account: 'beta', held: 1, slots: 0 }] };
    await g.mockApi(page, [[/\/api\/v1\/queue$/, q], ...g.defaultApi()]);
    await page.goto('/#/');
    const tile = page.getByRole('link', { name: /Running now/ });
    await expect(tile.locator('.stat-value')).toHaveText('1');
    await expect(tile).toContainText('2 waiting · 2 of 2 model slots busy');
    await expect(tile).toHaveAttribute('href', '#/queue');

    await g.mockApi(page, [[/\/api\/v1\/queue$/, q], [/\/api\/v1\/accounts$/, [g.accountSummary, { ...g.accountSummary, slug: 'beta' }]], ...g.defaultApi()]);
    await page.reload();
    await expect(tile).toHaveAttribute('href', '#/queue');
  });

  test('the overview reserves the queue tile and offers retry on failure', async ({ page }) => {
    await page.route('**/api/v1/queue', (route) => route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify(g.golden('error')) }));
    await page.goto('/#/');
    await expect(page.locator('table.account-breakdown tbody tr')).toHaveCount(1);
    await expect(page.getByRole('region', { name: 'Across all accounts' }).locator('.stat')).toHaveCount(3);
    await expect(page.getByRole('button', { name: 'Retry queue' })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Needs attention', exact: true })).toContainText('Queue alerts unavailable');
  });

  test("each account's row says how many of today's reviews are done, against its cap where it has one", async ({ page }) => {
    const a = g.accountSummary;
    const free = { ...a, slug: 'github/free', usage: { ...a.usage, reviewsToday: 7, reviewsPerDay: 0 } };
    const full = { ...a, slug: 'github/full', usage: { ...a.usage, reviewsToday: 48, reviewsPerDay: 50 } };
    await g.mockApi(page, [[/\/api\/v1\/accounts$/, [a, free, full]], ...g.defaultApi()]);
    await page.goto('/#/');
    const today = (slug: string) => page.getByRole('row').filter({ hasText: slug }).getByRole('cell').nth(4);
    await expect(page.locator('.account-breakdown').getByRole('columnheader').nth(4)).toHaveText('Today');
    await expect(today(a.slug)).toContainText(`${a.usage.reviewsToday} of ${a.usage.reviewsPerDay}`);
    await expect(today('github/free')).toHaveText('7');
    await expect(today('github/free').getByRole('meter')).toHaveCount(0);
    await expect(today('github/full').getByRole('meter')).toHaveClass(/tone-danger/);
  });

  test('the attention list says so when no review or queue issue needs a look', async ({ page }) => {
    await g.mockApi(page, [[/\/api\/v1\/queue$/, { jobs: [], slots: [] }], [/\/api\/v1\/accounts$/, [{ ...g.accountSummary, attention: { failed: 0, capped: 0, p0: 0, paused: 0 } }]], ...g.defaultApi()]);
    await page.goto('/#/');
    await expect(page.getByRole('region', { name: 'Needs attention', exact: true })).toContainText('All clear. No issues need attention.');
  });

  test('queue failures and full capacity share the attention list', async ({ page }) => {
    await g.mockApi(page, [[/\/api\/v1\/queue$/, { ...g.instanceQueue, jobs: [{ ...g.instanceQueue.jobs[0]!, state: 'discarded', recentFailure: true }] }], ...g.defaultApi()]);
    await page.goto('/#/');
    const attention = page.getByRole('region', { name: 'Needs attention', exact: true });
    await expect(attention.getByRole('link', { name: /At capacity/ })).toHaveAttribute('href', `#/a/${g.SLUG}/queue`);
    await expect(attention.getByRole('link', { name: /Failed job/ })).toHaveAttribute('href', `#/a/${g.SLUG}/queue?state=attention`);
    await expect(attention).not.toContainText('All clear');
  });

  test('a job waiting to retry is an alert here as it is on the account overview and the queue', async ({ page }) => {
    await g.mockApi(page, [[/\/api\/v1\/queue$/, { ...g.instanceQueue, jobs: [{ ...g.instanceQueue.jobs[0]!, kind: 'index', state: 'retryable', recentFailure: false }], slots: [] }], ...g.defaultApi()]);
    await page.goto('/#/');
    const attention = page.getByRole('region', { name: 'Needs attention', exact: true });
    await expect(attention.getByRole('link', { name: /Retrying/ })).toHaveAttribute('href', `#/a/${g.SLUG}/queue?state=attention`);
    await expect(attention).not.toContainText('All clear');
  });
});

test('a count of a thousand or more groups its digits, in every table', async ({ page }) => {
  const run = { ...g.repoDetail.indexRuns[0]!, chunkCount: 123456 };
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, { ...g.me, admin: true }],
    [/\/api\/v1\/admin\/accounts$/, [{ ...g.adminAccount, repositories: 1240, reviews7d: 10432 }]],
    [new RegExp(`/api/v1/accounts/${g.SLUG}/repos/alpha/one$`), { ...g.repoDetail, indexRuns: [run] }],
    [new RegExp(`/api/v1/accounts/${g.SLUG}/pulls$`), g.pageOf([{ ...g.pull, reviewCount: 1500, costUsd: 1234.5 }])],
    [new RegExp(`/api/v1/accounts/${g.SLUG}/rules$`), [{ ...g.rule, findings: 1204, addressed: 1100 }]],
    ...g.defaultApi(),
  ]);
  await page.goto('/#/admin');
  await expect(page.getByRole('row').filter({ hasText: g.adminAccount.slug }).first().locator('td.num')).toContainText(['1,240', '10,432']);
  await page.goto(`/${T}/repos/alpha/one`);
  await page.getByRole('button', { name: 'Index runs', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Index runs' }).locator('td.num')).toHaveText('123,456');
  await expect(page.getByRole('region', { name: 'Pull requests' }).locator('td.num')).toContainText(['1,500', '$1,234.50']);
  await page.goto(`/${T}/rules`);
  await expect(page.locator('.rule-cited')).toContainText('1,204');
  await expect(page.locator('.rule-cited')).toContainText('1,100 addressed');
});

test('a long account or App name is cut short, whole in its title, before a table of accounts runs past its card', async ({ page }) => {
  const slug = 'github/an-organization-with-a-very-long-name';
  const connection = 'an-app-with-a-very-long-name-too';
  const long = { slug, connection, attention: { failed: 12, capped: 3, p0: 40, paused: 7 }, lastWebhookAt: null };
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, { ...g.me, admin: true }],
    [/\/api\/v1\/accounts$/, [g.accountSummary, { ...g.accountSummary, ...long }]],
    [/\/api\/v1\/admin\/accounts$/, [g.adminAccount, { ...g.adminAccount, ...long }]],
    ...g.defaultApi(),
  ]);
  for (const h of ['#/', '#/admin']) {
    await page.goto(`/${h}`);
    const row = page.getByRole('row').filter({ hasText: 'an-organization' });
    await expect(row.locator('td.name-fill')).toHaveAttribute('title', slug);
    await expect(row.locator('td.name-clip')).toHaveAttribute('title', connection);
    expect(await row.locator('xpath=ancestor::div[contains(@class,"table-wrap")]').evaluate((el) => el.scrollWidth - el.clientWidth), h).toBe(0);
  }
  await page.goto('/#/');
  const wants = page.getByRole('row').filter({ hasText: 'an-organization' }).locator('.account-wants .pill');
  expect((await wants.nth(0).boundingBox())!.y).toBe((await wants.nth(1).boundingBox())!.y);
});

test('analytics shows the totals against the window before, the charts and the repositories', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(`/${T}/analytics`);
  const stats = page.locator('.analytics');
  const stat = (label: string) => stats.locator('.stat').filter({ has: page.getByText(label, { exact: true }) });
  const c = g.analytics.current;
  await expect(stat('Reviews').locator('.stat-value')).toHaveText(String(c.reviews));
  await expect(stat('Reviews').locator('.delta')).toHaveCount(0);
  await expect(stat('Reviews').locator('.stat-sub')).toHaveText(`${c.failed} failed · on ${c.pullRequests} pull requests`);
  // 2 of 6 findings, where the window before had none to compare.
  await expect(stat('Addressed').locator('.stat-value')).toHaveText('33%');
  await expect(stat('Addressed').locator('.stat-sub')).toHaveText(`2 of 6 findings · ${c.findings.p0} P0`);
  await expect(stat('Addressed').locator('.delta')).toHaveCount(0);
  await expect(stat('Median review').locator('.stat-value')).toHaveText('1m 30s');
  await expect(stat('Time to merge').locator('.stat-value')).toHaveText('36h');
  await expect(stat('Reactions').locator('.stat-value')).toHaveText(`${c.reactionsUp} up`);
  await expect(stat('Reactions').locator('.stat-sub')).toHaveText(`${c.reactionsDown} down`);
  await expect(stat('API spend').locator('.delta')).toHaveCount(0);
  await expect(stat('API spend').locator('.stat-sub')).toHaveText(/tokens$/);
  await expect(page.getByRole('img', { name: /^Completed reviews per day/ })).toBeVisible();
  await page.getByRole('combobox', { name: 'Trend metric' }).selectOption('findings');
  const findings = page.getByRole('region', { name: 'Review trends' });
  await expect(findings.getByRole('list', { name: /legend/ }).getByRole('listitem')).toHaveText(['P0', 'P1', 'P2']);
  await findings.getByRole('radio', { name: 'Table' }).click();
  await expect(findings.locator('tbody tr')).toHaveText([/Sep 1, 2026\s*1\s*2\s*3\s*6/]);
  await expect(page.getByRole('region', { name: 'Most reviewed repositories' }).locator('tbody tr')).toContainText(g.analytics.repositories[0]!.repository);

  await page.getByRole('radio', { name: '90 days' }).click();
  await expect.poll(() => seen.some((u) => u.pathname.endsWith('/analytics') && u.searchParams.get('group') === 'week')).toBe(true);
});

test('tables over time read newest first', async ({ page }) => {
  const point = g.analytics.series[0]!;
  const series = [point, { ...point, key: '2026-09-02', reviews: 7, findings: { p0: 4, p1: 0, p2: 0 } }];
  const day = g.usageSeries.rows[0]!;
  const call = g.reviewDetail.usage[0]!;
  await g.mockApi(page, [
    [/\/analytics$/, { ...g.analytics, series }],
    [/\/usage$/, { ...g.usageSeries, rows: [day, { ...day, key: '2026-09-02' }] }],
    [/\/reviews\/rev-1$/, { ...g.reviewDetail, usage: [call, { ...call, role: 'followup' }] }],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}/analytics`);
  const reviews = page.getByRole('region', { name: 'Review trends', exact: true });
  await reviews.getByRole('radio', { name: 'Table' }).click();
  await expect(reviews.locator('tbody tr')).toHaveText([/Sep 2, 2026\s*7/, /Sep 1, 2026\s*5/]);
  await page.getByRole('combobox', { name: 'Trend metric' }).selectOption('findings');
  const findings = page.getByRole('region', { name: 'Review trends' });
  await findings.getByRole('radio', { name: 'Table' }).click();
  await expect(findings.locator('tbody tr').first()).toHaveText(/Sep 2, 2026\s*4\s*0\s*0\s*4/);

  await page.goto(`/${T}/usage`);
  await expect(page.locator('tbody tr td:first-child')).toHaveText(['Sep 2, 2026', 'Sep 1, 2026']);
  await expect(page.locator('thead th').first()).toHaveText('Day');
  await expect(page.locator('tfoot td').nth(1)).toHaveAttribute('title', String(2 * day.inputTokens));

  await page.goto(`/${T}/reviews/rev-1/usage`);
  await expect(page.locator('tbody tr td:first-child')).toHaveText(['followup', call.role]);
});

test('the spend table names its group, and usage with none', async ({ page }) => {
  const day = g.usageSeries.rows[0]!;
  await g.mockApi(page, [[/\/usage$/, { ...g.usageSeries, group: 'repo', rows: [{ ...day, key: '' }] }], ...g.defaultApi()]);
  await page.goto(`/${T}/usage`);
  await expect(page.locator('thead th').first()).toHaveText('Repository');
  await expect(page.locator('tbody tr td:first-child')).toHaveText(['(none) →']);
});

test('a chart reads one column at a time, by pointer or by keyboard', async ({ page }) => {
  await page.goto(`/${T}/analytics`);
  const chart = page.getByRole('img', { name: /^Completed reviews per day/ });
  await chart.focus();
  await page.keyboard.press('ArrowRight');
  const tip = page.locator('.chart-tip');
  await expect(tip).toContainText('Sep 1');
  await expect(tip).toContainText(new RegExp(`${g.analytics.series[0]!.reviews}\\s+Reviews`));
  await page.keyboard.press('Escape');
  await expect(tip).toHaveCount(0);
  await chart.hover();
  await expect(tip).toContainText('Sep 1');
});

test("a chart of counts has whole numbers on its axis, and one of cost its cents", async ({ page }) => {
  const series = (reviews: number) => ({ ...g.analytics, series: [{ ...g.analytics.series[0]!, reviews }] });
  const ticks = page.getByRole('img', { name: /^Completed reviews per day/ }).locator('.chart-tick').filter({ hasText: /^[\d.,]+$/ });
  for (const [reviews, axis] of [
    [1, ['0', '1', '2']],
    [5, ['0', '3', '6']],
    [25, ['0', '20', '40']],
    [250, ['0', '125', '250']],
  ] as const) {
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/analytics$`), series(reviews)], ...g.defaultApi()]);
    await page.goto(`/${T}/analytics`);
    await expect(ticks).toHaveText([...axis]);
    await page.goto('about:blank');
  }
  await page.goto(`/${T}/usage`);
  await expect(page.locator('.chart-tick').filter({ hasText: '$' })).toHaveText(['$0', '$0.05', '$0.10']);
});

test('the spend page says what a review costs this month, median and mean, or that none completed', async ({ page }) => {
  const a = g.accountSummary;
  await g.mockApi(page, g.defaultApi());
  await page.goto(`/${T}/usage`);
  const spend = page.getByRole('region', { name: 'Totals' }).locator('.stat').filter({ hasText: 'API spend this month' });
  await expect(spend).toContainText('$1.50');
  await expect(spend).toContainText('$0.08 per review (median) · $0.10 mean of 12');

  const idle = { ...a, usage: { ...a.usage, reviews: 0, reviewCostUsd: 0, medianReviewCostUsd: null } };
  await g.mockApi(page, [[/\/api\/v1\/accounts$/, [idle]], ...g.defaultApi()]);
  await page.goto('about:blank');
  await page.goto(`/${T}/usage`);
  await expect(page.locator('.stat').filter({ hasText: 'API spend this month' })).toContainText('no review completed this month');
});

test("a chart's axis has room for its longest label", async ({ page }) => {
  const row = { ...g.usageSeries.rows[0]!, costUsd: 1234.56 };
  await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/usage$`), { ...g.usageSeries, rows: [row] }], ...g.defaultApi()]);
  await page.goto(`/${T}/usage`);
  const ticks = page.locator('.chart-tick').filter({ hasText: '$' });
  await expect(ticks).toHaveText(['$0', '$1,000.00', '$2,000.00']);
  const svg = (await page.getByRole('img', { name: /^API spend by day/ }).boundingBox())!;
  for (const t of await ticks.all()) expect((await t.boundingBox())!.x).toBeGreaterThanOrEqual(svg.x);
});

test('account overview links review alerts and capacity warnings to their next action', async ({ page }) => {
  const detail = g.golden<AccountDetail>('account_detail');
  const seen = await g.mockApi(page, [
    [/\/attention$/, g.attention],
    [new RegExp(`/api/v1/accounts/${g.SLUG}$`), { ...detail, usage: { ...detail.usage, tokens: 950_000, tokensPerMonth: 1_000_000, reviewsToday: 2, reviewsPerDay: 50 } }],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}`);
  const attention = page.getByRole('region', { name: 'Needs attention' });
  await expect(attention.getByRole('link', { name: /tokens are spent/ })).toHaveAttribute('href', `${T}/usage`);
  await expect(attention.getByRole('link', { name: /capped/ })).toHaveAttribute('href', `${T}/pulls?outcome=capped`);
  await attention.getByRole('link', { name: /P0/ }).click();
  await expect(page.getByRole('combobox', { name: 'Search pull requests' })).toHaveValue('is:p0');
  await expect.poll(() => seen.some((u) => u.pathname.endsWith('/pulls') && u.searchParams.get('is') === 'p0')).toBe(true);
});

test('account overview explains missing webhooks and the last poll', async ({ page }) => {
  const detail = g.golden<AccountDetail>('account_detail');
  const panel = page.getByRole('dialog', { name: 'Connection details', exact: true });
  await g.mockApi(page, [
    [/\/attention$/, { failed: 0, capped: 0, p0: 0, paused: 0 }],
    [/\/accounts\/github\/alpha\/queue$/, []],
    [new RegExp(`/api/v1/accounts/${g.SLUG}$`), { ...detail, connection: { ...detail.connection, lastWebhookAt: null } }],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}`);
  const attention = page.getByRole('region', { name: 'Needs attention', exact: true });
  await expect(attention).toContainText('No webhooks received');
  await expect(attention).not.toContainText('All clear');
  await expect(panel).toHaveCount(0);
  await page.getByRole('button', { name: 'Connection details', exact: true }).click();
  await expect(panel.getByRole('note')).toContainText(`GitHub App's webhook at ${g.meta.webUrl}${detail.connection.hookPath}`);
  await expect(panel.locator(`time[datetime="${detail.lastPolledAt}"]`)).toBeVisible();

  await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}$`), { ...detail, lastPolledAt: null, connection: { ...detail.connection, lastWebhookAt: null } }], ...g.defaultApi()]);
  await page.reload();
  await page.getByRole('button', { name: 'Connection details', exact: true }).click();
  await expect(panel).toContainText('not polled yet');
});

test("account overview explains unsigned webhooks", async ({ page }) => {
  const detail = g.golden<AccountDetail>('account_detail');
  const later = new Date(Date.parse(detail.connection.lastWebhookAt!) + 60_000).toISOString();
  await g.mockApi(page, [
    [new RegExp(`/api/v1/accounts/${g.SLUG}$`), { ...detail, connection: { ...detail.connection, lastUnsignedWebhookAt: later } }],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}`);
  const attention = page.getByRole('region', { name: 'Needs attention', exact: true });
  await expect(attention).toContainText('Unsigned webhooks refused');
  await attention.getByRole('button', { name: 'Fix connection', exact: true }).click();
  const note = page.getByRole('dialog', { name: 'Fix connection', exact: true }).getByRole('note');
  await expect(note).toContainText('with no signature');
  await expect(note).toContainText("Set the GitHub App's webhook secret");
});

test('repositories filter and repository detail', async ({ page }) => {
  await page.goto(`/${T}/repos`);
  await expect(page.locator('tbody tr')).toHaveCount(1);
  await page.getByPlaceholder('Filter by name').fill('nomatch');
  // The filter only sees the pages loaded so far, and the golden page has more.
  await expect(page.locator('.state-msg')).toContainText('Nothing loaded yet matches “nomatch”: load more to look further.');
  await expect(page.getByRole('combobox', { name: 'Type' }).locator('option').first()).toHaveText('In use');
  await page.getByRole('button', { name: 'Clear filter' }).click();
  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.getByPlaceholder('Filter by name')).toBeFocused();
  await page.getByPlaceholder('Filter by name').fill('alpha');
  await page.getByRole('link', { name: 'alpha/one' }).click();
  await expect(page).toHaveURL(new RegExp(`${T}/repos/alpha/one$`));
  await page.getByRole('button', { name: 'Repository settings', exact: true }).click();
  await expect(page.locator('.deflist').first()).toContainText(g.repoDetail.settings.ignore[0]!);
  await page.getByRole('button', { name: 'Index runs', exact: true }).click();
  await expect(page.locator('#repo-index').locator('../..')).toContainText(String(g.repoDetail.indexRuns[0]!.chunkCount));
  await expect(page.getByRole('region', { name: 'Pull requests' }).getByRole('link', { name: 'See all' })).toHaveAttribute('href', `${T}/pulls?state=all&repo=alpha%2Fone`);
  await expect(page.locator('#repo-pulls').locator('../..')).toContainText(g.pull.title);
});

test("a repository's limits group their digits", async ({ page }) => {
  const s = g.repoDetail.settings;
  const detail = { ...g.repoDetail, settings: { ...s, limits: { ...s.limits, tokensPerMonth: 2_500_000 } } };
  await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/repos/alpha/one$`), detail], ...g.defaultApi()]);
  await page.goto(`/${T}/repos/alpha/one`);
  await page.getByRole('button', { name: 'Repository settings', exact: true }).click();
  const value = (label: string) => page.locator('dt').filter({ hasText: label }).locator('+ dd');
  await expect(value('Max tokens')).toContainText('4,000,000');
  await expect(value('Tokens / month')).toContainText('2,500,000');
  await expect(value('Reviews / day')).toContainText('unlimited');
});

test("a repository's row sits on one line: the status with its time, the name with its switch, and one dash for nothing", async ({ page }) => {
  const r = g.repoPage.items[0]!;
  const bare = { ...r, id: 'repo-2', fullName: 'alpha/bare', index: { ...r.index, activeCommit: '' }, lastReview: null };
  await g.mockApi(page, [[/\/api\/v1\/me$/, { ...g.me, admin: true }], [new RegExp(`/api/v1/accounts/${g.SLUG}/repos$`), g.pageOf([r, bare])], ...g.defaultApi()]);
  await page.goto(`/${T}/repos`);
  const row = page.getByRole('row', { name: /alpha\/one/ });
  const middle = (sel: string) =>
    row.locator(sel).first().evaluate((el) => {
      const range = document.createRange();
      range.selectNodeContents(el);
      const box = range.getBoundingClientRect();
      return (box.top + box.bottom) / 2;
    });
  const word = await middle('.status-word');
  for (const sel of ['time', 'td.mono a', '.toggle > span:last-child']) expect(Math.abs((await middle(sel)) - word)).toBeLessThanOrEqual(1);

  const dashes = page.getByRole('row', { name: /alpha\/bare/ }).getByText('—', { exact: true });
  await expect(dashes).toHaveCount(1);
  await expect(dashes).not.toHaveClass(/mono/);
});

test('repositories say which are forks or archived', async ({ page }) => {
  const copy = { ...g.repoPage.items[0]!, id: 'repo-2', fullName: 'alpha/copy', fork: true, archived: true };
  await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/repos$`), g.pageOf([g.repoPage.items[0]!, copy])], ...g.defaultApi()]);
  await page.goto(`/${T}/repos`);
  await expect(page.getByRole('row', { name: /alpha\/copy/ }).locator('.badge')).toHaveText(['fork', 'archived']);
  await expect(page.getByRole('row', { name: /alpha\/one/ }).locator('.badge')).toHaveCount(0);
});

test('repository settings say where each comes from and what .kritika.yaml chose', async ({ page }) => {
  await page.goto(`/${T}/repos/alpha/one`);
  await page.getByRole('button', { name: 'Repository settings', exact: true }).click();
  const settings = page.locator('#repo-settings').locator('../..');
  const rc = g.repoDetail.repoConfig!;
  // The golden file chose another review model; the admin's is shown beside it.
  await expect(settings.getByText(rc.settings.models.review, { exact: true })).toBeVisible();
  await expect(settings).toContainText(`(.kritika.yaml; the admin's is ${g.repoDetail.settings.models.review})`);
  await expect(settings).toContainText(`Review effort ${rc.settings.models.effort} (default)`);
  await expect(settings).toContainText(`Confidence effort ${rc.settings.confidence.effort} (default)`);
  await expect(settings).toContainText(`Confidence fallback ${g.repoDetail.settings.confidence.fallback} (default)`);
  const tests = (c: { expr: string; paths?: string[] }) => [c.expr, c.paths?.length ? `paths ${c.paths.join(', ')}` : ''].filter(Boolean).join(' && ');
  const conditions = (cs: { name: string; expr: string; paths?: string[] }[]) => cs.map((c) => (c.name ? `${c.name}: ${tests(c)}` : tests(c))).join('; ') || '—';
  await expect(settings).toContainText(`Include ${conditions(rc.settings.filters.include)}`);
  await expect(settings).toContainText(`Exclude ${conditions(rc.settings.filters.exclude)} (account)`);
  const skills = rc.settings.skills;
  const narrows = (sc: { paths?: string[]; when?: { expr: string }[] }) => [sc.paths?.length ? `paths ${sc.paths.join(', ')}` : '', (sc.when ?? []).map((w) => w.expr).join(' or ')].filter(Boolean).join(' && ');
  await expect(settings).toContainText(`Skills ${skills.paths.join(', ') || 'off'}`);
  await expect(settings).toContainText(`Skill scopes ${Object.entries(skills.scope).map(([name, sc]) => `${name}${sc.load ? ' (loaded)' : ''}: ${narrows(sc)}`).join('; ') || '—'}`);
  await expect(settings).toContainText('Settle 30s (default)');
  const file = page.locator('#repo-file').locator('../..');
  await expect(file).toContainText(`Include${conditions(rc.filters.include)} (beside the admin's)`);
  await expect(file).toContainText(`Exclude${conditions(rc.filters.exclude)} (beside the admin's)`);
  await expect(file).toContainText(rc.dropped[0]!);
  await expect(file.getByRole('link', { name: 'the last review' })).toHaveAttribute('href', `#/a/${g.SLUG}/reviews/${rc.reviewId}`);
});

test.describe('pulls list', () => {
  test('filters, load more and keyboard navigation', async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/pulls`);
    const rows = page.locator('.pull-rows .pull-row');
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText(`${g.pull.repository} · #${g.pull.number} · ${g.pull.author}`);
    await expect(rows.first()).toContainText(`${g.pull.lastReview!.findings.p0} p0`);

    await page.getByRole('radio', { name: 'Closed' }).click();
    await expect.poll(() => seen.some((u) => u.pathname.endsWith('/pulls') && u.searchParams.get('state') === 'closed')).toBe(true);
    await expect(page.getByRole('radio', { name: 'Closed' })).toHaveAttribute('aria-checked', 'true');
    await page.getByRole('combobox', { name: 'Search pull requests' }).fill('status:failed author:ada widgets');
    await expect.poll(() => seen.some((u) => u.searchParams.get('outcome') === 'failed' && u.searchParams.get('author') === 'ada' && u.searchParams.get('q') === 'widgets')).toBe(true);

    await page.getByRole('button', { name: 'Load more' }).click();
    await expect.poll(() => seen.some((u) => u.searchParams.get('cursor') === g.repoPage.nextCursor)).toBe(true);
    await expect(rows).toHaveCount(2);

    // '?' in the search box is the box's, not the help overlay's.
    await page.getByRole('combobox', { name: 'Search pull requests' }).focus();
    await page.keyboard.press('?');
    await expect(page.locator('.help-card')).toHaveCount(0);

    await page.locator('main h1').click();
    await page.keyboard.press('j');
    await expect(rows.first()).toHaveClass(/selected/);
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(new RegExp(`${T}/pulls/alpha/one/7$`));
  });

  test('the search box suggests filter tokens and their values', async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/pulls`);
    const search = page.getByRole('combobox', { name: 'Search pull requests' });
    const options = page.getByRole('listbox', { name: 'Suggestions' }).getByRole('option');
    await search.focus();
    await expect(options).toHaveText([/^repo:/, /^author:/, /^status:/, /^is:/]);
    await expect(search).toHaveAttribute('aria-expanded', 'true');

    await search.pressSequentially('st');
    await expect(options).toHaveText([/^status:/]);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Tab');
    await expect(search).toHaveValue('status:');
    await search.pressSequentially('fai');
    await expect(options).toHaveText(['status:failed']);
    await page.keyboard.press('ArrowDown');
    await expect(search).toHaveAttribute('aria-activedescendant', (await options.first().getAttribute('id'))!);
    await page.keyboard.press('Enter');
    await expect(search).toHaveValue('status:failed ');
    await expect.poll(() => seen.some((u) => u.pathname.endsWith('/pulls') && u.searchParams.get('outcome') === 'failed')).toBe(true);

    await search.pressSequentially('repo:');
    await options.filter({ hasText: g.repoPage.items[0]!.fullName }).click();
    await expect(search).toHaveValue(`status:failed repo:${g.repoPage.items[0]!.fullName} `);
    await expect.poll(() => seen.some((u) => u.searchParams.get('repo') === g.repoPage.items[0]!.fullName)).toBe(true);

    await page.keyboard.press('Escape');
    await expect(search).toHaveAttribute('aria-expanded', 'false');
  });

  test('token suggestions dismiss without changing the filter and reopen on focus', async ({ page }) => {
    await page.goto(`/${T}/pulls`);
    const search = page.getByRole('combobox', { name: 'Search pull requests' });
    await search.fill('status:');
    await search.press('Tab');
    await expect(search).not.toBeFocused();
    await expect(search).toHaveValue('status:');
    await search.focus();
    await search.press('ArrowDown');
    await search.press('Escape');
    await expect(search).toHaveAttribute('aria-expanded', 'false');
    await expect(search).toHaveValue('status:');
    await expect(search).toBeFocused();
    await search.press('Tab');
    await expect(search).not.toBeFocused();
    await search.focus();
    await expect(page.getByRole('listbox', { name: 'Suggestions' })).toBeVisible();
    await search.fill('no matching token');
    await expect(search).toHaveAttribute('aria-expanded', 'false');
    await expect(search).not.toHaveAttribute('aria-activedescendant', /.+/);
    await search.press('Tab');
    await expect(search).toHaveValue('no matching token');
    await expect(search).not.toBeFocused();
  });

  test('an unknown status is not applied, but a repository outside the suggestions is', async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/pulls`);
    await page.getByRole('combobox', { name: 'Search pull requests' }).fill('repo:nope/nope status:great');
    await expect(page.getByRole('note')).toHaveText('Not filtering by status:great: nothing by that name.');
    await expect.poll(() => seen.some((u) => u.searchParams.get('repo') === 'nope/nope')).toBe(true);
    expect(seen.some((u) => u.searchParams.has('outcome'))).toBe(false);
    await expect(page).toHaveURL(/repo=nope%2Fnope$/);
  });

  test('a click anywhere on a row opens and closes its preview; the title opens the pull request', async ({ page }) => {
    await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/pulls`);
    const row = page.locator('.pull-rows .pull-row').first();
    await row.getByText('ada', { exact: false }).click();
    await expect(page.getByRole('region', { name: 'Preview of alpha/one#7', exact: true })).toBeVisible();
    // The space to the right of the title is the row's, not the link's.
    const cell = (await row.locator('td.pull-main').boundingBox())!;
    await row.locator('td.pull-main').click({ position: { x: cell.width - 8, y: 12 } });
    await expect(page.getByRole('region', { name: 'Preview of alpha/one#7', exact: true })).toHaveCount(0);
    await expect(page).toHaveURL(new RegExp(`${T}/pulls$`));
    await row.getByRole('link', { name: 'Add widgets' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/pulls/alpha/one/7$`));
  });

  test('filters live in the URL: a reload keeps them, and Back from a pull returns to them', async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/pulls`);
    const search = page.getByRole('combobox', { name: 'Search pull requests' });
    await page.getByRole('radio', { name: 'All' }).click();
    await search.fill('wid gets status:failed');
    await expect(page).toHaveURL(new RegExp(`${T}/pulls\\?state=all&outcome=failed&q=wid\\+gets$`));

    // The box keeps what was typed rather than the URL's order.
    await expect(search).toHaveValue('wid gets status:failed');

    await page.reload();
    await expect(page.getByRole('radio', { name: 'All' })).toHaveAttribute('aria-checked', 'true');
    await expect(search).toHaveValue('status:failed wid gets');
    const last = () => seen.filter((u) => u.pathname.endsWith('/pulls')).at(-1)?.searchParams;
    await expect.poll(() => last()?.get('q')).toBe('wid gets');
    expect(last()?.get('state')).toBe('all');
    expect(last()?.get('outcome')).toBe('failed');

    await page.locator('.pull-rows .pull-title').first().click();
    await expect(page).toHaveURL(new RegExp(`${T}/pulls/alpha/one/7$`));
    await page.goBack();
    await expect(page).toHaveURL(/\?state=all&outcome=failed&q=wid\+gets$/);
    await expect(search).toHaveValue('status:failed wid gets');

    // The section's tab is the unfiltered list, search box included.
    await page.getByRole('navigation', { name: 'Sections' }).getByRole('link', { name: 'Pull requests' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/pulls$`));
    await expect(search).toHaveValue('');
    await expect(page.getByRole('radio', { name: 'Open' })).toHaveAttribute('aria-checked', 'true');
  });

  test('an empty list says whether its filters emptied it, and clears them', async ({ page }) => {
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/pulls$`), (u: URL) => g.pageOf(u.searchParams.has('outcome') ? [] : [g.pull])], ...g.defaultApi()]);
    await page.goto(`/${T}/pulls?outcome=failed`);
    await expect(page.locator('.state-msg')).toHaveText(/No pull requests match these filters\./);
    await page.getByRole('button', { name: 'Clear filters' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/pulls$`));
    await expect(page.locator('.pull-rows .pull-row')).toHaveCount(1);
    await expect(page.getByRole('combobox', { name: 'Search pull requests' })).toBeFocused();

    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/pulls$`), g.pageOf([])], ...g.defaultApi()]);
    await page.reload();
    await expect(page.locator('.state-msg')).toHaveText('No open pull requests.');
    await expect(page.getByRole('button', { name: 'Clear filters' })).toHaveCount(0);
  });

  test('each pull request shows how many of its reviews completed and what they all cost', async ({ page }) => {
    const none = { ...g.pull, number: 12, url: g.pull.url.replace(/\d+$/, '12'), lastReview: null, reviewCount: 0, costUsd: 0 };
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/pulls$`), g.pageOf([g.pull, none])], ...g.defaultApi()]);
    await page.goto(`/${T}/pulls`);
    await expect(page.getByRole('columnheader')).toContainText(['Pull request', 'Findings', 'Last review', 'Reviews', 'API spend']);
    await expect(page.getByRole('columnheader', { name: 'Updated', exact: true })).toHaveCount(0);
    const rows = page.locator('.pull-rows .pull-row');
    await expect(rows.nth(0).locator('td.num')).toContainText([String(g.pull.reviewCount), '$0.84']);
    await expect(rows.nth(1).locator('td.num')).toContainText(['0', '$0']);
  });

  test('a pull request whose automatic reviews are paused says so, in the list and on its page', async ({ page }) => {
    const paused: Pull = { ...g.pull, paused: true };
    await g.mockApi(page, [[/\/pulls$/, g.pageOf([paused, { ...g.pull, number: 8, url: g.pull.url.replace(/\d+$/, '8') }])], [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, pull: paused }], ...g.defaultApi()]);
    await page.goto(`/${T}/pulls`);
    const rows = page.locator('.pull-rows tr');
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0).locator('.badge')).toHaveText('paused');
    await expect(rows.nth(1).locator('.badge')).toHaveCount(0);
    await page.goto(`/${T}/pulls/alpha/one/7`);
    await expect(page.locator('.paused-notice')).toContainText('Automatic reviews are paused');
  });

  test('a row marks a pull request that is merged, closed or a draft, and its page says which', async ({ page }) => {
    const as = (number: number, more: Partial<Pull>): Pull => ({ ...g.pull, number, url: g.pull.url.replace(/\d+$/, String(number)), ...more });
    const merged = as(7, { state: 'closed', merged: true });
    await g.mockApi(page, [
      [/\/pulls$/, g.pageOf([merged, as(8, { state: 'closed' }), as(9, { draft: true }), as(10, {})])],
      [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, pull: merged }],
      ...g.defaultApi(),
    ]);
    await page.goto(`/${T}/pulls?state=all`);
    const marks = page.locator('.pull-rows tr .lifecycle');
    await expect(marks).toHaveCount(3);
    expect(await marks.evaluateAll((els) => els.map((e) => e.getAttribute('title')))).toEqual(['Merged', 'Closed', 'Draft']);
    await expect(marks.nth(0)).toHaveClass(/tone-merged/);
    await expect(marks.nth(1)).toHaveClass(/tone-muted/);
    await page.goto(`/${T}/pulls/alpha/one/7`);
    await expect(page.locator('.lifecycle-badge')).toHaveText('Merged');
    await expect(page.locator('.lifecycle-badge')).toHaveClass(/tone-merged/);
  });

  test("a fork's pull request not reviewed reads like any other", async ({ page }) => {
    const fork = { ...g.pull, number: 12, url: g.pull.url.replace(/\d+$/, '12'), fork: true, lastReview: null };
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/pulls$`), g.pageOf([fork, { ...g.pull, lastReview: null }])], ...g.defaultApi()]);
    await page.goto(`/${T}/pulls`);
    const rows = page.locator('.pull-rows .pull-row');
    await expect(rows.nth(0)).toContainText('not reviewed');
    await expect(rows.nth(0)).not.toContainText('fork');
    await expect(rows.nth(1)).toContainText('not reviewed');
  });

  test('the keyboard cursor stays on its pull when a live refetch adds one above it', async ({ page }) => {
    const newer = { ...g.pull, number: 9, title: 'Newer widgets', url: g.pull.url.replace(/\d+$/, '9') };
    let added = false;
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/pulls$`), () => g.pageOf(added ? [newer, g.pull] : [g.pull])], ...g.defaultApi()]);
    await page.goto(`/${T}/pulls`);
    const rows = page.locator('.pull-rows .pull-row');
    await expect(rows).toHaveCount(1);
    await page.locator('main h1').click();
    await page.keyboard.press('j');
    await expect(rows.first()).toHaveClass(/selected/);

    // The fixture's stream closes as it opens, and every reopen refetches.
    added = true;
    await expect(rows).toHaveCount(2);
    await expect(rows.first()).not.toHaveClass(/selected/);
    await expect(rows.nth(1)).toHaveClass(/selected/);
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(new RegExp(`${T}/pulls/alpha/one/7$`));
  });
});

test.describe('findings', () => {
  test('lists each finding with its pull request and whether it was addressed', async ({ page }) => {
    await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/findings`);
    const f = g.accountFinding;
    const row = page.locator('.finding-row').first();
    await expect(row).toContainText(f.title);
    await expect(row).toContainText(f.explanation);
    await expect(row.locator('.sev')).toHaveText(f.severity);
    await expect(row.getByRole('link', { name: `${f.pull.repository}#${f.pull.number}` })).toHaveAttribute('href', `#/a/${g.SLUG}/pulls/alpha/one/7`);
    await expect(row.locator('.status-word')).toHaveText(f.status);
    await expect(row.getByTitle('Reactions to its comment on GitHub')).toHaveText(`${f.reactionsUp} ${f.reactionsDown}`);
    await expect(row.getByRole('link', { name: 'Thread on GitHub' })).toHaveAttribute('href', `${f.pull.url}#discussion_r${f.forgeCommentId}`);
    await expect(row.locator('.finding-rules').getByRole('link')).toHaveText(f.rules);
    await expect(page.locator('.sections .section-tab.active')).toHaveText('Findings');
    await expect(page.getByRole('navigation', { name: 'Sections' }).getByRole('link', { name: 'Findings', exact: true })).toHaveAttribute('aria-current', 'page');

    await row.locator('.finding-sub').first().click();
    await expect(page.getByRole('dialog', { name: 'Finding details' })).toContainText(f.explanation);
    await page.getByRole('link', { name: 'Open review', exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/reviews/${f.reviewId}\\?finding=${f.id}$`));
    await expect(page.locator(`#finding-${f.id}`)).toBeFocused();
  });

  test('the status control narrows the list, and an open finding stands out from a dismissed one', async ({ page }) => {
    const f = g.accountFinding;
    const list = [
      { ...f, id: 'f-o', status: 'open' },
      { ...f, id: 'f-d', status: 'dismissed', dismissReason: 'house style' },
    ];
    await g.mockApi(page, [[/\/findings$/, g.pageOf(list)], ...g.defaultApi()]);
    await page.goto(`/${T}/findings`);
    const rows = page.locator('.finding-row');
    await expect(rows.nth(0).locator('.status')).toHaveClass(/tone-accent/);
    await expect(rows.nth(1).locator('.status')).toHaveClass(/tone-muted/);
    await expect(rows.nth(1)).toContainText('house style');
    const status = page.getByRole('radiogroup', { name: 'Status' });
    await expect(status.getByRole('radio', { name: 'All' })).toBeChecked();
    await status.getByRole('radio', { name: 'Open' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/findings\\?status=open$`));
    await expect(page.getByRole('combobox', { name: 'Search findings' })).toHaveValue('status:open');
    await status.getByRole('radio', { name: 'All' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/findings$`));
  });

  test('j, k and Enter move a cursor down the list and open its finding', async ({ page }) => {
    const f = g.accountFinding;
    await g.mockApi(page, [[/\/findings$/, g.pageOf([f, { ...f, id: 'f-2', title: 'second' }])], ...g.defaultApi()]);
    await page.goto(`/${T}/findings`);
    const rows = page.locator('.finding-row');
    await expect(rows).toHaveCount(2);
    await page.keyboard.press('j');
    await page.keyboard.press('j');
    await expect(rows.nth(1)).toHaveClass(/selected/);
    await page.keyboard.press('k');
    await expect(rows.nth(0)).toHaveClass(/selected/);
    await page.keyboard.press('/');
    await expect(page.getByRole('combobox', { name: 'Search findings' })).toBeFocused();
    await page.getByRole('combobox', { name: 'Search findings' }).blur();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog', { name: 'Finding details' })).toContainText(f.title);
    await expect(page).toHaveURL(new RegExp(`${T}/findings$`));
  });

  test('filters by severity, status and repository tokens, in the URL', async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/findings`);
    const search = page.getByRole('combobox', { name: 'Search findings' });
    await search.fill(`severity:p0 status:addressed repo:${g.repoPage.items[0]!.fullName} deref`);
    await expect(page).toHaveURL(new RegExp(`${T}/findings\\?severity=p0&status=addressed&repo=alpha%2Fone&q=deref$`));
    const last = () => seen.filter((u) => u.pathname.endsWith('/findings')).at(-1)?.searchParams;
    await expect.poll(() => last()?.get('severity')).toBe('p0');
    expect(last()?.get('status')).toBe('addressed');
    expect(last()?.get('repo')).toBe(g.repoPage.items[0]!.fullName);
    expect(last()?.get('q')).toBe('deref');

    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/findings$`), g.pageOf([])], ...g.defaultApi()]);
    await page.reload();
    await expect(search).toHaveValue(`repo:${g.repoPage.items[0]!.fullName} severity:p0 status:addressed deref`);
    await expect(page.locator('.state-msg')).toContainText('No findings match these filters.');
    await page.getByRole('button', { name: 'Clear filters' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/findings$`));
    await expect(search).toHaveValue('');
    await expect(search).toBeFocused();
  });

  test('a cited rule narrows the list to the findings that cite it', async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    await page.goto(`/${T}/findings`);
    const id = g.accountFinding.rules[0]!;
    await page.locator('.finding-row').first().getByRole('link', { name: id }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/findings\\?rule=${id}$`));
    await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/findings')).at(-1)?.searchParams.get('rule')).toBe(id);
    await expect(page.getByRole('combobox', { name: 'Search findings' })).toHaveValue(`rule:${id}`);
  });
});

test.describe('rules', () => {
  test('lists each rule reviews check, where it is set and which repositories read it', async ({ page }) => {
    const file: Rule = { ...g.rule, kind: 'context', id: '', text: '', path: 'db/schema.sql', description: 'the schema', source: 'repository' };
    const fileRule: Rule = { ...g.rule, id: 'house-style', text: '', path: '.kritika/review.md', source: 'repository' };
    const renovate: Rule = { ...g.rule, id: 'renovate', text: 'Say what the update breaks.', paths: [], when: [{ name: '', expr: 'pr.headRef.startsWith("renovate/")' }, { name: 'deps', expr: 'pr.headRef.startsWith("deps/")' }] };
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/rules$`), [file, g.rule, fileRule, renovate]], ...g.defaultApi()]);
    await page.goto(`/${T}/rules`);
    const r = g.rule;
    const rows = page.locator('.rule-table tbody tr');
    await expect(rows).toHaveCount(4);
    // A rule with conditions shows them in place of every change.
    await expect(rows.nth(2).locator('.rule-when code')).toHaveText(renovate.when.map((w) => w.expr));
    await expect(rows.nth(2).locator('.rule-when').nth(1)).toContainText('or deps:');
    await expect(rows.nth(2)).not.toContainText('every change');
    // A file rule names its file over its id.
    await expect(rows.nth(1).locator('.rule-path')).toHaveText(fileRule.path);
    await expect(rows.nth(1).locator('.rule-sub')).toHaveText(fileRule.id);
    const row = rows.first();
    await expect(row.locator('.rule-body')).toHaveText(r.text);
    await expect(row.locator('.rule-sub')).toHaveText(r.id);
    await expect(row.locator('code')).toHaveText(r.paths);
    await expect(row).toContainText('Repository entry');
    await expect(row.getByRole('link', { name: r.repositories[0]! })).toHaveAttribute('href', `#/a/${g.SLUG}/repos/alpha/one`);
    // Its findings, narrowed to its one repository.
    await expect(row.locator('.rule-cited')).toContainText(`${r.addressed} addressed`);
    await expect(row.getByRole('link', { name: String(r.findings), exact: true })).toHaveAttribute(
      'href',
      `#/a/${g.SLUG}/findings?repo=alpha%2Fone&rule=${r.id}`,
    );
    await expect(rows.last().locator('.rule-path')).toHaveText(file.path);
    await expect(rows.last()).toContainText('.kritika.yaml');
    await expect(rows.last().locator('.rule-cited')).toHaveCount(0);
    await expect(page.locator('.sections .section-tab.active')).toHaveText('Rules');

    const search = page.getByRole('combobox', { name: 'Search rules' });
    await search.fill('kind:context package');
    await expect(page.locator('.state-msg')).toHaveText('No rule matches.');
    await search.fill('kind:rule package');
    await expect(rows).toHaveCount(1);
    await expect(rows.locator('.rule-sub')).toHaveText(r.id);
    await search.fill('renovate/');
    await expect(rows.locator('.rule-sub')).toHaveText(renovate.id);
  });

  test('a rule keeps room for its words on a phone, where the table scrolls sideways instead', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto(`/${T}/rules`);
    const main = page.locator('.rule-main').first();
    await expect(main).toBeVisible();
    expect((await main.boundingBox())!.width).toBeGreaterThanOrEqual(240);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
  });

  test("a rule file's long path breaks after a slash or a hyphen, not inside a word", async ({ page }) => {
    const file: Rule = { ...g.rule, id: 'style', text: '', path: 'docs/contributing/code-style-and-review-guidelines.md' };
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/rules$`), [file]], ...g.defaultApi()]);
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto(`/${T}/rules`);
    const lines = await page.locator('.rule-path').evaluate((el) => {
      const text = el.firstChild as Text;
      const out: string[] = [];
      let top = -1;
      for (let i = 0; i < text.length; i++) {
        const r = document.createRange();
        r.setStart(text, i);
        r.setEnd(text, i + 1);
        const t = Math.round(r.getBoundingClientRect().top);
        if (t !== top) out.push('');
        top = t;
        out[out.length - 1] += text.data[i];
      }
      return out;
    });
    expect(lines.length).toBeGreaterThan(1);
    for (const line of lines.slice(0, -1)) expect(line).toMatch(/[/-]$/);
  });

  test('says how to add a rule when there is none', async ({ page }) => {
    await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/rules$`), []], ...g.defaultApi()]);
    await page.goto(`/${T}/rules`);
    await expect(page.locator('.state-msg')).toContainText('Add rules in your configuration or .kritika.yaml.');
  });
});

test('pull workspace leads with the latest review and keeps skipped attempts in activity', async ({ page }) => {
  const golden = g.pullDetail.reviews[0]!;
  const first = { ...golden, confidence: { ...golden.confidence!, reason: `${golden.confidence!.reason} See [up/stream#12](https://redirect.github.com/up/stream/issues/12).` } };
  await g.mockApi(page, [
    [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [{ ...first, id: 'rev-2', status: 'skipped', skipReason: 'filtered' }, first] }],
    [/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, confidence: first.confidence } }],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}/pulls/alpha/one/7`);
  await expect(page.locator('h1')).toContainText(g.pullDetail.pull.title);
  const p = g.pullDetail.pull;
  await page.getByRole('button', { name: 'Pull request details', exact: true }).click();
  const details = page.getByRole('dialog', { name: 'Pull request details' });
  await expect(details).toContainText(`${p.author} wants to merge ${p.headRef} into ${p.baseRef}`);
  await expect(details).not.toContainText('last synchronized');
  await page.keyboard.press('Escape');
  const latest = page.getByRole('region', { name: 'Review summary', exact: true });
  await expect(latest).toContainText(g.reviewDetail.summary!.take);
  const c = golden.confidence!;
  await latest.getByRole('button', { name: /^Confidence/ }).click();
  await expect(page.getByRole('dialog', { name: /^Confidence/ }).locator('.confidence-line')).toHaveText(`Confidence ${c.score}/5, below the ${c.threshold} this repository asks for\u00a0· ${c.risk} risk: ${c.reason} See up/stream#12.`);
  await expect(page.getByRole('dialog', { name: /^Confidence/ }).locator('.confidence-line').getByRole('link', { name: 'up/stream#12' })).toHaveAttribute('href', 'https://redirect.github.com/up/stream/issues/12');
  await expect(page.getByRole('dialog', { name: /^Confidence/ }).locator('.confidence-line')).toHaveAttribute('title', `scored by ${c.model}`);
  await page.keyboard.press('Escape');
  const f = g.reviewDetail.findings[0]!;
  await page.getByRole('button', { name: `Details for ${f.title}`, exact: true }).click();
  await expect(page.locator('.finding').getByRole('link', { name: 'Thread on GitHub' })).toHaveAttribute('href', `${p.url}#discussion_r${f.forgeCommentId}`);
  await expect(page.locator('.pr-workspace-head')).toContainText('Latest attempt skipped: excluded by a trigger condition');
  await page.getByRole('navigation', { name: 'Review sections' }).getByRole('link', { name: 'Activity', exact: true }).click();
  await expect(page.locator('.timeline-item')).toHaveCount(2);
  await expect(page.locator('.timeline-item').last()).toContainText('$0.42');
  await expect(page.locator('.followup')).toContainText(g.followup.author);
  await page.getByRole('button', { name: 'Transcript' }).click();
  await expect(page.locator('.followup .turn')).toHaveCount(g.transcript.turns.length);
  await page.locator('.timeline-link').last().click();
  await expect(page).toHaveURL(new RegExp(`${T}/pulls/alpha/one/7\\?run=rev-1$`));
  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Review history' })).toBeVisible();
});

test.describe('review', () => {
  test('merge impact shows evidence separately from findings', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1`);
    const impact = page.getByRole('region', { name: 'Merge impact' });
    await expect(impact).toBeVisible();
    await expect(impact).toContainText('migrations/002.sql');
    await expect(impact).toContainText('reverting the binary does not restore its data');
    await expect(page.locator('.review-findings [data-finding]')).toHaveCount(g.reviewDetail.findings.length);
    await expect(page.locator('.finding')).toHaveCount(0);
  });

  test('merge impact preserves fenced commands', async ({ page }) => {
    await g.mockApi(page, [
      [/\/reviews\/rev-1$/, { ...g.reviewDetail, summary: { ...g.reviewDetail.summary!, impact: ['Verify startup:\n\n```sh\nkubectl get pods\n```\nExpect all pods ready.'] } }],
      ...g.defaultApi(),
    ]);
    await page.goto(`/${T}/reviews/rev-1`);
    const impact = page.getByRole('region', { name: 'Merge impact' });
    await expect(impact.locator('pre')).toHaveText('kubectl get pods');
    await expect(impact.locator('li')).toHaveCount(1);
  });

  for (const [name, impact] of [['old summary', undefined], ['empty impact', []]] as const) {
    test(`merge impact is absent for ${name}`, async ({ page }) => {
      await g.mockApi(page, [
        [/\/reviews\/rev-1$/, { ...g.reviewDetail, summary: { ...g.reviewDetail.summary!, impact } }],
        ...g.defaultApi(),
      ]);
      await page.goto(`/${T}/reviews/rev-1`);
      await expect(page.locator('#sum-take')).toBeVisible();
      await expect(page.getByRole('region', { name: 'Merge impact' })).toHaveCount(0);
    });
  }

  test('summary groups findings; tabs switch', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1`);
    await expect(page.locator('#sum-take').locator('../..')).toContainText(g.reviewDetail.summary!.take);
    const f = g.reviewDetail.findings[0]!;
    await expect(page.locator('.review-findings .sev')).toHaveText(f.severity);
    const toggle = page.getByRole('button', { name: `Details for ${f.title}`, exact: true });
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.focus();
    await page.keyboard.press('Enter');
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await expect(page.locator('.finding')).toContainText(f.title);
    await expect(page.locator('.finding')).toContainText(`${f.path}:${f.line}-${f.endLine}`);
    await expect(page.locator('.finding .code-block')).toContainText(f.replacement);
    await expect(page.locator('.finding-head .badge.mono')).toHaveText(f.rules);
    // The golden finding was posted inline as comment 55 on the pull request.
    await expect(page.locator('.finding').getByRole('link', { name: 'Thread on GitHub' })).toHaveAttribute(
      'href',
      `${g.reviewDetail.review.pull.url}#discussion_r${f.forgeCommentId}`,
    );
    const r = g.reviewDetail.review;
    await page.getByRole('button', { name: /^Confidence/ }).click();
    await expect(page.getByRole('dialog', { name: /^Confidence/ }).locator('.confidence-line')).toContainText(`${r.confidence!.risk} risk: ${r.confidence!.reason}`);
    await page.keyboard.press('Escape');
    await page.getByRole('combobox', { name: 'Run details' }).selectOption('timeline');
    const fact = (name: string) => page.locator('.run-details .facts > div').filter({ has: page.getByRole('term').getByText(name, { exact: true }) }).getByRole('definition');
    await expect(fact('Scope')).toHaveText(new RegExp(`^${r.scope}\\s\\(${r.scopeReason}\\)$`));
    await expect(fact('Risk')).toHaveText(r.confidence!.risk);
    await expect(fact('Confidence')).toHaveText(new RegExp(`^${r.confidence!.score}/5\\s\\(below ${r.confidence!.threshold}\\)$`));
    await expect(fact('Model')).toHaveText(r.model);

    for (const [tab, text] of [
      ['Timeline', g.reviewDetail.runnerRun!.podName],
      ['Raw', '.kritika.yaml'],
      ['Usage', 'Total'],
    ] as const) {
      await page.getByRole('combobox', { name: 'Run details' }).selectOption(tab.toLowerCase());
      await expect(page).toHaveURL(new RegExp(`/reviews/rev-1/${tab.toLowerCase()}$`));
      await expect(page.locator('.tab-panel')).toContainText(text);
    }
    await expect(page.getByRole('combobox', { name: 'Run details' })).toHaveValue('usage');
    await page.getByRole('combobox', { name: 'Run details' }).selectOption('timeline');
    const a = g.reviewDetail.agentRun!;
    const skills = page.locator('.deflist').filter({ hasText: 'Skills' }).getByRole('definition').filter({ hasText: a.skillsOffered[0]! });
    await expect(skills).toHaveText(`${a.skillsOffered.join(', ')} (read ${a.skillsOpened.join(', ')})`);
    await expect(skills.locator('.mono.muted')).toHaveText(a.skillsOffered.filter((s) => !a.skillsOpened.includes(s)));
    const commands = page.locator('.deflist').filter({ hasText: 'Commands' }).getByRole('definition').filter({ hasText: a.commandsOffered[0]! });
    await expect(commands).toHaveText(`${a.commandsOffered.join(', ')} (ran ${a.commandsRun.join(', ')})`);
    await expect(commands.locator('.mono.muted')).toHaveText(a.commandsOffered.filter((c) => !a.commandsRun.includes(c)));
    // The golden run carried on the conversation of the review before.
    const carried = page.locator('.deflist').getByRole('definition').filter({ hasText: 'carried on from' });
    await expect(carried).toHaveText('carried on from the review before');
    await expect(carried.getByRole('link')).toHaveAttribute('href', `${T}/reviews/${a.carriedReviewId}/conversation`);
  });

  test('a finding says when a later review dropped it or a maintainer dismissed it', async ({ page }) => {
    const f = g.reviewDetail.findings[0]!;
    const findings = [
      { ...f, id: 'f-a', status: 'addressed' as const },
      { ...f, id: 'f-d', status: 'dismissed' as const, dismissReason: 'house style' },
      { ...f, id: 'f-o' },
    ];
    await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, findings }], ...g.defaultApi()]);
    await page.goto(`/${T}/reviews/rev-1`);
    await expect(page.locator('.review-findings [data-finding]')).toHaveCount(3);
    for (const row of await page.locator('.review-findings [data-finding]').all()) await row.getByRole('button').click();
    await expect(page.locator('#finding-f-a .pill')).toHaveText('addressed');
    await expect(page.locator('#finding-f-d .pill')).toHaveText('dismissed');
    await expect(page.locator('#finding-f-d')).toContainText('Dismissed: house style');
    await expect(page.locator('#finding-f-o .pill')).toHaveCount(0);
    await page.goto(`/${T}/pulls/alpha/one/7`);
    await expect(page.locator('.review-findings [data-finding]')).toHaveCount(3);
    for (const row of await page.locator('.review-findings [data-finding]').all()) await row.getByRole('button').click();
    await expect(page.locator('#finding-f-a .pill')).toHaveText('addressed');
    await expect(page.locator('#finding-f-d')).toContainText('Dismissed: house style');
    await expect(page.locator('#finding-f-o')).toContainText(f.title);
  });

  test('a review that is no longer the latest leads to the one that is', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1`);
    await expect(page.locator('.page-head h1')).toBeVisible();
    await expect(page.locator('.newer-notice')).toHaveCount(0);
    await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, newestReviewId: 'rev-2' } }], ...g.defaultApi()]);
    await page.reload();
    await expect(page.locator('.newer-notice').getByRole('link', { name: 'Open its latest review' })).toHaveAttribute('href', `${T}/pulls/alpha/one/7`);
  });

  test('the summary lists the rules the review checked, and how many findings cite each', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1`);
    await page.getByRole('button', { name: /^Rules checked/ }).click();
    const rules = page.getByRole('region', { name: 'Rules checked', exact: true }).first();
    await expect(rules.getByRole('listitem')).toHaveText([/wrap-errors\s*1 finding/, /no-tokens\s*no finding/]);
    await expect(rules.getByRole('link', { name: 'wrap-errors' })).toHaveAttribute('href', `${T}/findings?rule=wrap-errors`);
    const pack = { ...g.reviewDetail.contextPack!, ruleIds: [] };
    await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, contextPack: pack }], ...g.defaultApi()]);
    await page.reload();
    await expect(page.locator('.page-head h1')).toBeVisible();
    await expect(rules).toHaveCount(0);
  });

  test('code is highlighted where its language is known, with the same text, and left plain where it is not', async ({ page }) => {
    const errors: string[] = [];
    page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
    await page.goto(`/${T}/reviews/rev-1/raw`);
    // The submitted result is JSON, and the golden log tail is no language.
    const result = page.getByRole('region', { name: 'Submitted result' }).locator('pre');
    await expect(result).toHaveAttribute('data-lang', 'json');
    await expect(result.locator('span').first()).toBeVisible();
    await expect(result).toHaveText(JSON.stringify(g.reviewRaw.result, null, 2));
    const log = page.getByRole('region', { name: 'Log tail' }).locator('pre');
    await expect(log).not.toHaveAttribute('data-lang');
    await expect(log.locator('span')).toHaveCount(0);

    // A finding's replacement takes its language from the finding's file.
    await page.goto(`/${T}/reviews/rev-1`);
    const f = g.reviewDetail.findings[0]!;
    await page.getByRole('button', { name: `Details for ${f.title}`, exact: true }).click();
    const replacement = page.locator(`#finding-${f.id} pre`).first();
    await expect(replacement).toHaveAttribute('data-lang', 'go');
    await expect(replacement).toHaveText(f.replacement);
    expect(errors).toEqual([]);
  });

  test('the diff highlights a file whose language it knows, line for line', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1/diff`);
    const added = page.locator('table.diff tr.dl-add td.code');
    await expect(added.locator('span:not(.sign)').first()).toBeVisible();
    await expect(added).toHaveText('+var x *int');
    await expect(page.locator('table.diff tr.dl-hunk td.code span:not(.sign)')).toHaveCount(0);
  });

  test("a finding's prose renders its lists, tables and emphasis, and its html as text", async ({ page }) => {
    const f = g.reviewDetail.findings[0]!;
    const explanation = [
      'The handler *never* checks `x`:',
      '',
      '- it is read on line 3',
      '- [docs](https://example.com/nil) say it may be nil',
      '',
      '| case | result |',
      '|---|---|',
      '| nil | panic |',
      '',
      '<img src=x onerror="document.title=1"> ![chart](https://example.com/c.png)',
    ].join('\n');
    await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, findings: [{ ...f, explanation }] }], ...g.defaultApi()]);
    await page.goto(`/${T}/reviews/rev-1`);
    await page.getByRole('button', { name: `Details for ${f.title}`, exact: true }).click();
    const md = page.locator(`#finding-${f.id} .md`).first();
    await expect(md.locator('em')).toHaveText('never');
    await expect(md.locator('ul > li')).toHaveText(['it is read on line 3', 'docs say it may be nil']);
    await expect(md.getByRole('link', { name: 'docs' })).toHaveAttribute('href', 'https://example.com/nil');
    await expect(md.locator('table.md-table tbody td')).toHaveText(['nil', 'panic']);
    // The html is text, and the image a link: nothing is loaded or run.
    await expect(md).toContainText('<img src=x onerror="document.title=1">');
    await expect(md.locator('img')).toHaveCount(0);
    await expect(md.getByRole('link', { name: 'chart' })).toHaveAttribute('href', 'https://example.com/c.png');
    expect(await page.title()).not.toBe('1');
  });

  test('diff anchors a finding under its line', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1/diff`);
    const anchored = page.locator('tr.dl-finding');
    await expect(anchored).toHaveCount(1);
    await expect(anchored).toContainText(g.reviewDetail.findings[0]!.title);
    // The row right above the finding is new-side line 3, and the only one marked.
    await expect(anchored.locator('xpath=preceding-sibling::tr[1]')).toContainText('var x *int');
    await expect(page.locator('tr.dl-marked')).toHaveCount(1);
    await expect(page.locator('tr.dl-marked')).toContainText('var x *int');
    await page.locator('.diff-file-head').getByRole('button', { name: /a\.go/ }).click();
    await expect(page.locator('table.diff')).toHaveCount(0);
  });

  test('conversation shows tool calls as pretty JSON and truncated results', async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1/conversation`);
    const turn = page.locator('.turn').first();
    await expect(turn.locator('.tool-call pre')).toHaveText(JSON.stringify(g.transcript.turns[0]!.messages[0]!.toolCalls[0]!.input, null, 2));
    await expect(turn.locator('.badge-warn')).toContainText('truncated 10 B');
    await expect(turn).toContainText(g.transcript.turns[0]!.response.text);
    await page.getByRole('button', { name: /^System prompt/ }).click();
    await expect(page.locator('.conversation-top')).toContainText(g.transcript.system);
    await page.getByRole('button', { name: 'raw JSON' }).click();
    await expect(turn.locator('pre')).toContainText('"runnerRunId"');
    await page.getByPlaceholder('Filter turns').fill('no-such-text');
    await expect(page.locator('.turn')).toHaveCount(0);
  });

  test("a review that carried on another's conversation says so and links to it", async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1/conversation`);
    await expect(page.locator('.turn').first()).toBeVisible();
    await expect(page.locator('.carried-notice')).toHaveCount(0);
    const turn = g.transcript.turns[0]!;
    const carried = { ...g.transcript, turns: [{ ...turn, messagesFrom: 5, carriedReviewId: 'rev-0' }, { ...turn, id: 'mc-2', index: 1, step: 1 }] };
    await g.mockApi(page, [[/\/reviews\/rev-1\/transcript$/, carried], ...g.defaultApi()]);
    await page.goto(`/${T}/reviews/rev-1/conversation`);
    const note = page.locator('.carried-notice');
    await expect(note).toHaveText('This review carried on the conversation of the review before; its earlier messages are there, not repeated here.');
    await note.getByRole('link', { name: 'the review before' }).click();
    await expect(page).toHaveURL(new RegExp(`${T}/reviews/rev-0/conversation$`));
    const afresh = { ...g.reviewDetail, agentRun: { ...g.reviewDetail.agentRun!, carriedReviewId: null } };
    await g.mockApi(page, [[/\/reviews\/rev-1$/, afresh], ...g.defaultApi()]);
    await page.goto(`/${T}/reviews/rev-1/timeline`);
    await expect(page.locator('.deflist').filter({ hasText: 'Model' })).not.toContainText('carried on from');
  });

  test("a split review's timeline lists its parts and names each step by its part", async ({ page }) => {
    await page.goto(`/${T}/reviews/rev-1/timeline`);
    await expect(page.locator('.chart-tick').first()).toBeVisible();
    await expect(page.getByRole('table', { name: 'Parts' })).toHaveCount(0);
    const a = g.reviewDetail.agentRun!;
    const step = a.timeline[0]!;
    const part = a.parts[0]!;
    const agentRun = {
      ...a,
      steps: 3,
      timeline: [
        { ...step, index: 0, part: 1 },
        { ...step, index: 1, part: 1 },
        { ...step, index: 2, part: 2 },
      ],
      parts: [
        { ...part, paths: ['server/a.ts', 'server/b.ts', 'web/c.ts'], steps: 2 },
        { ...part, paths: ['docs/d.md'], stop: 'no_submit', error: 'answered in prose', steps: 1 },
      ],
    };
    await g.mockApi(page, [[/\/reviews\/rev-1$/, { ...g.reviewDetail, agentRun }], ...g.defaultApi()]);
    await page.goto(`/${T}/reviews/rev-1/timeline`);
    await expect(page.locator('section[aria-labelledby="tl-agent"] .panel-head')).toContainText('3 steps in 2 parts');
    const rows = page.getByRole('table', { name: 'Parts' }).locator('tbody tr');
    await expect(rows.nth(0).locator('td')).toHaveText(['1', '3 files in server/, web/', '2', 'submitted', '']);
    await expect(rows.nth(1).locator('td')).toHaveText(['2', '1 file in docs/', '1', 'no_submit', 'answered in prose']);
    const labels = page.getByRole('img', { name: /^Tokens per agent step/ }).locator('.chart-tick').filter({ hasText: /^\d+\.\d+$/ });
    await expect(labels).toHaveText(['1.0', '1.1', '2.0']);
  });

  test("a split review's conversation shows each part's turns together, then those after them", async ({ page }) => {
    const turn = g.transcript.turns[0]!;
    const transcript = {
      ...g.transcript,
      turns: [
        { ...turn, id: 'mc-1', index: 0, part: 1, step: 0 },
        { ...turn, id: 'mc-2', index: 1, part: 2, step: 0 },
        { ...turn, id: 'mc-3', index: 2, part: 1, step: 1 },
        { ...turn, id: 'mc-4', index: 3, part: 0, step: 0, kind: 'merge' as const },
      ],
    };
    await g.mockApi(page, [[/\/reviews\/rev-1\/transcript$/, transcript], ...g.defaultApi()]);
    await page.goto(`/${T}/reviews/rev-1/conversation`);
    await expect(page.locator('.part-head')).toHaveText(['Part 1', 'Part 2', 'After the parts']);
    await expect(page.locator('.turn-title')).toHaveText(['#0', '#2', '#1', '#3']);
    await expect(page.getByRole('heading', { level: 4, name: '#2' })).toBeVisible();
  });
});

test('a skipped review says why, on its own page and on the pull request', async ({ page }) => {
  const skipped = { ...g.pullDetail.reviews[0]!, status: 'skipped' as const, skipReason: 'too_large' as const };
  await g.mockApi(page, [
    [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [skipped] }],
    [/\/reviews\/rev-1$/, { ...g.reviewDetail, review: { ...g.reviewDetail.review, status: 'skipped', skipReason: 'unchanged_patch' } }],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}/pulls/alpha/one/7`);
  await expect(page.locator('.page-inner > .notice')).toContainText('Latest attempt skipped: more changed lines than the repository allows');
  await expect(page.getByText('Not reviewed yet.')).toBeVisible();
  await page.goto(`/${T}/reviews/rev-1`);
  await expect(page.getByLabel('Review outcome')).toContainText('No review performed. patch unchanged since the last review');
});

test('a skipped review with no reason of its own says what it recorded on the pull request', async ({ page }) => {
  const skipped = { ...g.pullDetail.reviews[0]!, status: 'skipped' as const, skipReason: '' as const, error: 'the pull request is closed' };
  await g.mockApi(page, [[/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, reviews: [skipped] }], ...g.defaultApi()]);
  await page.goto(`/${T}/pulls/alpha/one/7`);
  await expect(page.locator('.page-inner > .notice')).toContainText('Latest attempt skipped: the pull request is closed');
});

test('no tab strip scrolls vertically', async ({ page }) => {
  await page.goto(`/${T}/reviews/rev-1`);
  await expect(page.locator('nav.tabs')).toBeVisible();
  for (const strip of await page.locator('.sections, .tabs').all()) {
    expect(await strip.evaluate((n) => n.scrollHeight - n.clientHeight)).toBe(0);
  }
});

test('pull detail says what its unfinished review job waits on, and nothing of one that simply runs', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  await page.goto(`/${T}/pulls/alpha/one/7`);
  const j = g.job;
  const notice = page.locator('.job-notice');
  await expect(notice).toContainText('A review is waiting to run again. GitHub did not answer.');
  await expect(notice).not.toContainText(j.lastError);
  await notice.getByRole('button', { name: 'Queue details' }).click();
  const details = page.getByRole('dialog', { name: 'Queue details' });
  await expect(details).toContainText(`Attempt ${j.attempt} of ${j.maxAttempts} failed, the next was due`);
  await expect(details).toContainText(j.lastError);
  await page.keyboard.press('Escape');
  await expect(notice.getByRole('link', { name: 'Open the queue' })).toHaveAttribute('href', `#/a/${g.SLUG}/queue`);

  const pull = /\/pulls\/alpha\/one\/7$/;
  await g.mockApi(page, [[pull, { ...g.pullDetail, job: { ...j, state: 'running' } }], ...g.defaultApi()]);
  await page.reload();
  await expect(notice).toContainText(`A review is running, attempt ${j.attempt} of ${j.maxAttempts}.`);

  await g.mockApi(page, [[pull, { ...g.pullDetail, job: { ...j, state: 'available', attempt: 0, lastError: '', cause: '' } }], ...g.defaultApi()]);
  await page.reload();
  await expect(notice).toHaveText('A review is queued. Open the queue');

  await g.mockApi(page, [[pull, { ...g.pullDetail, job: { ...j, state: 'running', attempt: 1, lastError: '', cause: '' } }], ...g.defaultApi()]);
  await page.reload();
  await expect(page.locator('h1')).toContainText(g.pullDetail.pull.title);
  await expect(notice).toHaveCount(0);
});

test('a time still to come says how far off it is', async ({ page }) => {
  const due = new Date(Date.now() + 10 * 60_000).toISOString();
  const waiting = { ...g.job, scheduledAt: due };
  await g.mockApi(page, [[/\/queue$/, [waiting]], [/\/pulls\/alpha\/one\/7$/, { ...g.pullDetail, job: waiting }], ...g.defaultApi()]);
  await page.goto(`/${T}/queue`);
  await expect(page.locator(`tbody td[data-label="Activity"] time[datetime="${due}"]`)).toHaveText('in 10m');
  await page.goto(`/${T}/pulls/alpha/one/7`);
  await page.getByRole('button', { name: 'Queue details' }).click();
  await expect(page.getByRole('dialog', { name: 'Queue details' })).toContainText('failed, the next is due in 10m.');
});

test("a run's phases say the second each began and ended, not only how long ago", async ({ page }) => {
  await page.goto(`/${T}/reviews/rev-1/timeline`);
  const times = page.locator('#tl-runner').locator('xpath=ancestor::section').locator('tbody time');
  await expect(times.first()).toHaveText(/\d{1,2}:\d{2}:\d{2}/);
  await expect(times.first()).not.toHaveAttribute('title');
});

test('names are set in mono, and words and numbers in the text face', async ({ page }) => {
  const mono = /(^| )mono( |$)/;
  await page.goto(`/${T}/findings`);
  const f = g.accountFinding;
  await expect(page.getByRole('link', { name: `${f.pull.repository}#${f.pull.number}` })).toHaveClass(mono);
  await page.goto(`/${T}/queue`);
  const cells = page.locator('tbody tr').first().locator('td');
  await expect(cells.nth(0)).toContainText(`#${g.job.id} · review`);
  await expect(cells.nth(0)).not.toHaveClass(mono);
  await expect(cells.nth(0).getByRole('link')).toHaveClass(mono);
  const role = { ...g.usageSeries, group: 'role', rows: [{ ...g.usageSeries.rows[0]!, key: 'review' }] };
  await g.mockApi(page, [[/\/usage$/, (u: URL) => (u.searchParams.get('group') === 'role' ? role : { ...role, group: 'model', rows: [{ ...role.rows[0]!, key: 'acme/large' }] })], ...g.defaultApi()]);
  await page.goto(`/${T}/usage`);
  await expect(page.locator('tbody td').first()).toHaveClass(mono);
  await page.getByRole('combobox', { name: 'Group by' }).selectOption('role');
  await expect(page.locator('tbody td').first()).toHaveText('review');
  await expect(page.locator('tbody td').first()).not.toHaveClass(mono);
});

test("the instance's queue lists every account's jobs and the model slots they wait on", async ({ page }) => {
  const q = g.instanceQueue;
  const other = { ...q.jobs[0]!, id: 43, kind: 'thread' as const, account: 'github/beta', args: { repository: 'beta/two', number: 3, head: '', trigger: '', commentId: 910, resolved: false } };
  await g.mockApi(page, [[/\/api\/v1\/queue$/, { ...q, jobs: [...q.jobs, other, { ...other, id: 44, args: { ...other.args, resolved: true } }], slots: [...q.slots, { account: 'github/beta', model: 'acme/small', held: 1, slots: 0 }] }], ...g.defaultApi()]);
  await page.goto('/#/queue');
  await expect(page.locator('.page-head h1')).toHaveText('Queue');
  await expect(page.locator('.account-button')).toHaveText('All accounts');
  const slots = page.getByRole('region', { name: 'Model slots' }).locator('tbody tr');
  await expect(slots).toHaveText([/github\/alpha\s*openrouter\/acme-large\s*2 of 2/, /github\/beta\s*acme\/small\s*1, no limit/]);
  await expect(page.getByRole('meter', { name: 'Slots of openrouter/acme-large busy for github/alpha' })).toHaveAttribute('aria-valuenow', '2');
  const jobs = page.locator('main > .page-inner > .table-wrap tbody tr');
  await expect(jobs).toHaveCount(3);
  await expect(jobs.nth(2)).toContainText('Resolve thread');
  await expect(jobs.nth(1)).toContainText('#43 · thread');
  await expect(jobs.nth(1)).toContainText('Restore thread');
  await expect(jobs.nth(1).getByRole('link', { name: 'github/beta' })).toHaveAttribute('href', '#/a/github/beta/queue');
  await expect(jobs.nth(1).getByRole('link', { name: 'beta/two#3' })).toHaveAttribute('href', '#/a/github/beta/pulls/beta/two/3');
});

test("a job's error keeps a readable width however wide the columns beside it, and takes none when there is no error", async ({ page }) => {
  const long = { ...g.instanceQueue.jobs[0]!, id: 43, account: 'github/an-account-with-a-very-long-name-indeed', lastError: 'runner: the pod exceeded its deadline after 20m0s and was deleted; the last log line was "waiting for a model slot"' };
  const fine = { ...g.instanceQueue.jobs[0]!, id: 44, lastError: '', cause: '' };
  await g.mockApi(page, [[/\/api\/v1\/queue$/, { ...g.instanceQueue, jobs: [long, fine] }], ...g.defaultApi()]);
  await page.setViewportSize({ width: 900, height: 800 });
  await page.goto('/#/queue');
  await page.getByRole('button', { name: 'Details for job #43' }).click();
  const details = page.getByRole('region', { name: 'Job #43 details' });
  expect((await details.locator('.error-text').boundingBox())!.width).toBeGreaterThanOrEqual(260);
  await expect(details).toContainText(long.lastError);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);

  await g.mockApi(page, [[/\/api\/v1\/queue$/, { ...g.instanceQueue, jobs: [fine] }], ...g.defaultApi()]);
  await page.reload();
  await expect(page.getByRole('button', { name: 'Details for job #44' })).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('.row-preview')).toHaveCount(0);
});

test("the palette finds every account's recent pull requests, the current account's first, and the instance's queue", async ({ page }) => {
  const beta = { ...g.pull, repository: 'beta/two', number: 3, title: 'Beta gadgets', updatedAt: '2026-09-02T00:00:00Z' };
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, { ...g.me, accounts: [g.SLUG, 'github/beta'] }],
    [/\/accounts\/github\/beta\/pulls$/, g.pageOf([beta])],
    ...g.defaultApi(),
  ]);
  await page.goto(`/${T}/rules`);
  await expect(page.locator('.account-button')).toHaveText(g.SLUG);
  await page.keyboard.press('Control+k');
  const input = page.locator('.palette-input input');
  await input.fill('#');
  // The other account's pull request is the more recently updated one.
  await expect(page.locator('.palette .row-sub')).toHaveText([`${g.pull.repository}#${g.pull.number}`, 'beta/two#3']);
  await input.fill('gadgets');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/a\/github\/beta\/pulls\/beta\/two\/3$/);
  await page.keyboard.press('Control+k');
  await page.locator('.palette-input input').fill('slots');
  await expect(page.locator('.palette .row-title')).toHaveText(['Queue']);
});

test.describe('your settings', () => {
  const started = g.reviewDetail.review.createdAt;
  const hover = (page: import('@playwright/test').Page) => page.getByRole('dialog', { name: 'Pull request details' }).locator(`time[datetime="${started}"]`).first();

  test('a time zone and a clock are saved as chosen, and every time follows them', async ({ page }) => {
    const sent = await g.mockWrites(page, [['PUT', /\/api\/v1\/me\/settings$/, { status: 204 }]]);
    await page.goto('/#/');
    await page.locator('.user-button').click();
    await page.getByRole('link', { name: 'Settings', exact: true }).click();
    await expect(page).toHaveURL(/#\/settings$/);
    await expect(page.locator('.page-head h1')).toHaveText('Your settings');

    await page.getByRole('combobox', { name: 'Time zone', exact: true }).fill('Tokyo');
    await page.getByRole('option', { name: 'Asia/Tokyo', exact: true }).click();
    await page.getByRole('radio', { name: '24-hour', exact: true }).click();
    await expect.poll(() => sent.map((s) => s.body)).toEqual([
      { timeZone: 'Asia/Tokyo', clock: '', theme: '' },
      { timeZone: 'Asia/Tokyo', clock: '24', theme: '' },
    ]);
    await expect(page.getByText(/^Now: .* GMT\+9$/)).toBeVisible();

    // In-app navigation: no reload, so the choice just made is what applies.
    await page.locator('.brand').click();
    await page.evaluate((h) => (location.hash = h), `${T}/pulls/alpha/one/7`);
    await page.getByRole('button', { name: 'Pull request details', exact: true }).click();
    await expect(hover(page)).toHaveAttribute('title', 'Sep 1, 2026, 21:00:00 GMT+9');
  });

  test('settings kept with the user apply from the first page', async ({ page }) => {
    await g.mockApi(page, [[/\/api\/v1\/me$/, { ...g.me, settings: { timeZone: 'UTC', clock: '12', theme: '' } }], ...g.defaultApi()]);
    await page.goto(`/${T}/pulls/alpha/one/7`);
    await page.getByRole('button', { name: 'Pull request details', exact: true }).click();
    await expect(hover(page)).toHaveAttribute('title', 'Sep 1, 2026, 12:00:00 PM UTC');
    await page.goto('/#/settings');
    await expect(page.getByLabel('Time zone')).toHaveValue('UTC');
    await expect(page.getByRole('radio', { name: '12-hour', exact: true })).toBeChecked();
  });

  test('a choice the server refuses is taken back, and says so', async ({ page }) => {
    await g.mockWrites(page, [['PUT', /\/api\/v1\/me\/settings$/, g.apiError(400, 'bad_request', 'clock must be "12", "24" or empty')]]);
    await page.goto('/#/settings');
    await page.getByRole('radio', { name: '24-hour', exact: true }).click();
    await expect(page.getByRole('status')).toContainText('Not saved');
    await expect(page.getByRole('radiogroup', { name: 'Clock' }).getByRole('radio', { name: 'Auto' })).toBeChecked();

    // A refused theme leaves the page as it looked.
    const html = page.locator('html');
    const was = await html.getAttribute('class');
    await page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: 'Dark' }).click();
    await expect(page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: 'Auto' })).toBeChecked();
    await expect(html).toHaveAttribute('class', was ?? '');
  });

  test('a theme is kept with the user, from the page or the top bar, and applies wherever they sign in', async ({ page }) => {
    const sent = await g.mockWrites(page, [['PUT', /\/api\/v1\/me\/settings$/, { status: 204 }]]);
    await page.goto('/#/settings');
    const themes = page.getByRole('radiogroup', { name: 'Theme' });
    await themes.getByRole('radio', { name: 'Dark' }).click();
    await expect(page.locator('html')).toHaveClass(/dark/);
    await expect.poll(() => sent.at(-1)?.body).toEqual({ timeZone: '', clock: '', theme: 'dark' });

    // The top bar's button steps dark to auto, which is the browser's own.
    await page.getByRole('button', { name: 'Toggle theme' }).click();
    await expect.poll(() => sent.at(-1)?.body).toEqual({ timeZone: '', clock: '', theme: '' });
    await expect(themes.getByRole('radio', { name: 'Auto' })).toBeChecked();
    await page.getByRole('button', { name: 'Toggle theme' }).click();
    await expect.poll(() => sent.at(-1)?.body).toEqual({ timeZone: '', clock: '', theme: 'light' });
    await expect(themes.getByRole('radio', { name: 'Light' })).toBeChecked();

    // Another browser, whose own preference is light: the user's dark wins.
    await page.evaluate(() => localStorage.setItem('kritika-theme', 'light'));
    await g.mockApi(page, [[/\/api\/v1\/me$/, { ...g.me, settings: { ...g.NO_SETTINGS, theme: 'dark' } }], ...g.defaultApi()]);
    await page.reload();
    await expect(page.locator('html')).toHaveClass(/dark/);
    await expect(themes.getByRole('radio', { name: 'Dark' })).toBeChecked();
  });
});

test('the top bar fits a phone: the logo keeps its size and the user menu stays on screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 720 });
  await page.goto(`/${T}/pulls`);
  const user = (await page.locator('.user-button').boundingBox())!;
  expect(user.x + user.width).toBeLessThanOrEqual(390);
  expect((await page.locator('.brand .logo').boundingBox())!.width).toBe(22);
});

test("the usage tiles fill their grid at every width, with no cell left empty", async ({ page }) => {
  for (const [width, columns] of [
    [1280, 4],
    [600, 2],
    [390, 1],
  ] as const) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto(`/${T}/usage`);
    const tiles = page.getByRole('region', { name: 'Totals' });
    await expect(tiles.locator(':scope > div')).toHaveCount(4);
    expect(await tiles.evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(' ').length)).toBe(columns);
  }
});

test('your settings fit a phone: no control runs past the screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 720 });
  await page.goto('/#/settings');
  for (const control of [page.locator('#pref-zone'), ...(await page.locator('.prefs .segmented').all())]) {
    const box = (await control.boundingBox())!;
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  }
});

test('queue, usage, follow-ups and admin console pages render their fixtures', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  await page.goto(`/${T}/queue`);
  await page.getByRole('button', { name: `Details for job #${g.job.id}` }).click();
  await expect(page.locator('.queue-table')).toContainText('GitHub did not answer.');
  await expect(page.locator('.row-preview .error-text')).toBeVisible();
  await expect(page.locator('.row-preview')).toContainText(g.job.lastError);
  await expect(page.locator('.row-preview')).toContainText(`${g.job.attempt}/${g.job.maxAttempts}`);

  await page.goto(`/${T}/usage`);
  await expect(page.locator('tbody tr')).toContainText('Sep 1, 2026');
  await expect(page.getByRole('img', { name: /API spend by day/ })).toBeVisible();
  await expect(page.getByRole('meter', { name: 'Monthly tokens used' })).toHaveAttribute('aria-valuemax', String(g.accountSummary.usage.tokensPerMonth));
  await expect(page.getByRole('region', { name: 'Totals' })).toContainText('$1.50');
  await page.getByRole('radio', { name: '7 days' }).click();
  await page.getByRole('combobox', { name: 'Group by' }).selectOption('model');
  await expect.poll(() => seen.some((u) => u.pathname.endsWith('/usage') && u.searchParams.get('group') === 'model')).toBe(true);

  await page.goto(`/${T}/followups`);
  await expect(page.locator('.followup')).toContainText(`${g.followup.repository}#${g.followup.number}`);
  // The golden follow-up was asked in a thread, and answered there.
  const fu = g.followup;
  await expect(page.locator('.followup').getByRole('link', { name: 'The question on GitHub' })).toHaveAttribute('href', `${fu.pullUrl}#discussion_r${fu.commentId}`);
  await expect(page.locator('.followup').getByRole('link', { name: "kritika's reply" })).toHaveAttribute('href', `${fu.pullUrl}#discussion_r${fu.replyCommentId}`);
  await g.mockApi(page, [[/\/followups$/, g.pageOf([{ ...fu, inline: false, status: 'ignored', reason: 'not a maintainer', replyCommentId: null }])], ...g.defaultApi()]);
  await page.reload();
  await expect(page.locator('.followup').getByRole('link', { name: 'The question on GitHub' })).toHaveAttribute('href', `${fu.pullUrl}#issuecomment-${fu.commentId}`);
  await expect(page.locator('.followup').getByRole('link', { name: "kritika's reply" })).toHaveCount(0);
  await expect(page.locator('.followup-reason')).toHaveText('not a maintainer');

  await page.goto('/#/admin');
  await expect(page.getByRole('row').filter({ hasText: g.adminAccount.slug })).toContainText('not served');
});

test('a server-sent event for the account refetches the page', async ({ page }) => {
  const seen = await g.mockApi(page, g.defaultApi());
  // No resync opens this stream, so only the event can refetch.
  await page.route('**/api/events', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: `event: ${g.liveEvent.kind}\ndata: ${JSON.stringify({ ...g.liveEvent, account: g.SLUG })}\n\n`,
    }),
  );
  await page.goto(`/${T}/queue`);
  await expect.poll(() => seen.filter((u) => u.pathname.endsWith('/queue')).length).toBeGreaterThan(1);
});

for (const [label, entry] of [
  ["the page's own build", () => g.builtEntry()],
  ['no build', () => ''],
] as const) {
  test(`a stream that (re)opens refetches the page, its resync naming ${label}`, async ({ page }) => {
    const seen = await g.mockApi(page, g.defaultApi());
    let opens = 0;
    await page.route('**/api/events', (route) => {
      opens++;
      return route.fulfill({ status: 200, contentType: 'text/event-stream', body: g.resyncFrame(entry()) });
    });
    let loads = 0;
    page.on('request', (r) => {
      if (r.isNavigationRequest()) loads++;
    });
    await page.goto(`/${T}/queue`);
    const queues = () => seen.filter((u) => u.pathname.endsWith('/queue')).length;
    // The first fetch, then one refetch per open: the reconnect backoff
    // (at least 500ms) outlasts live()'s 300ms debounce, so none merge.
    await expect.poll(() => opens).toBeGreaterThan(1);
    await expect.poll(queues).toBeGreaterThanOrEqual(3);
    expect(loads).toBe(1);
  });
}

test('a stream from a server running another build reloads the page instead of refetching it', async ({ page }) => {
  await g.mockApi(page, g.defaultApi());
  const log: string[] = [];
  let fetched!: () => void;
  const firstFetch = new Promise<void>((r) => (fetched = r));
  page.on('request', (r) => {
    if (r.isNavigationRequest()) log.push('page');
    else if (new URL(r.url()).pathname.endsWith(`/accounts/${g.SLUG}/queue`)) {
      log.push('queue');
      fetched();
    }
  });
  let opens = 0;
  // The first stream, once the page has fetched, comes from a server that
  // runs another build; the page reloads into the build it serves.
  await page.route('**/api/events', async (route) => {
    const first = ++opens === 1;
    if (first) await firstFetch;
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body: g.resyncFrame(first ? g.resync.entry : g.builtEntry()) });
  });
  await page.goto(`/${T}/queue`);
  const loads = () => log.filter((l) => l === 'page').length;
  await expect.poll(loads).toBe(2);
  const reload = log.lastIndexOf('page');
  expect(log.slice(0, reload)).toEqual(['page', 'queue']);
  // The reloaded page runs the server's build, so its resync refetches.
  await expect.poll(() => log.slice(reload).filter((l) => l === 'queue').length).toBeGreaterThanOrEqual(2);
  expect(loads()).toBe(2);
});

test('the user menu shows the version the server reports', async ({ page }) => {
  await page.goto(`/${T}/analytics`);
  await page.locator('.user-button').click();
  await expect(page.locator('.user-panel .user-version')).toHaveText(`kritika ${g.meta.version}`);
});

test('each page names itself in the browser tab', async ({ page }) => {
  const r = g.reviewDetail.review;
  for (const [h, title] of [
    ['#/', 'All accounts · kritika'],
    [`${T}/analytics`, `Analytics · ${g.SLUG} · kritika`],
    [`${T}/repos/alpha/one`, 'alpha/one · kritika'],
    [`${T}/pulls?outcome=failed`, `Pull requests · ${g.SLUG} · kritika`],
    [`${T}/pulls/alpha/one/7`, `${g.pullDetail.pull.title} · alpha/one#7 · kritika`],
    [`${T}/reviews/rev-1/diff`, `${r.status} · ${r.pull.repository}#${r.pull.number} review · kritika`],
    [`${T}/queue`, `Queue · ${g.SLUG} · kritika`],
    ['#/admin', 'Instance settings · kritika'],
  ]) {
    await page.goto(`/${h}`);
    await expect(page).toHaveTitle(title);
  }
});

test("a running review's tab title says when it is done", async ({ page }) => {
  let done = false;
  const review = (status: ReviewStatus) => ({ ...g.reviewDetail, review: { ...g.reviewDetail.review, status } });
  await g.mockApi(page, [[new RegExp(`/api/v1/accounts/${g.SLUG}/reviews/rev-1$`), () => review(done ? 'completed' : 'running')], ...g.defaultApi()]);
  await page.goto(`/${T}/reviews/rev-1`);
  await expect(page).toHaveTitle(/^running · /);
  // The fixture's stream reopens, and every reopen refetches.
  done = true;
  await expect(page).toHaveTitle(/^completed · /);
});

test('a signed-in user navigating to sign-in is sent back', async ({ page, mockProviders }) => {
  await mockProviders();
  await page.goto(`/${T}/queue`);
  await expect(page.locator('tbody tr')).toHaveCount(1);
  await page.evaluate(() => (location.hash = '#/signin'));
  await expect(page).toHaveURL(/#\/$/);
  await expect(page.locator('.page-head h1')).toHaveText('Operations overview');
  await expect(page.locator('.signin-card')).toHaveCount(0);
});

test('a page longer than the window scrolls in one place, not the document as well', async ({ page }) => {
  for (const h of [T, `${T}/repos/alpha/one`, '#/admin']) {
    await page.goto(`/${h}`);
    await expect(page.locator('.state-msg[aria-live]')).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollHeight - innerHeight)).toBe(0);
  }
});

test('nothing slides for a viewer who asked for less motion', async ({ page }) => {
  await page.goto(`/${T}/repos`);
  const one = page.getByRole('switch').first();
  await expect(one).toHaveCSS('transition-duration', '0.15s');
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await expect(one).toHaveCSS('transition-duration', '0s');
});

test('a tab stays where it is whichever tab is the current one', async ({ page }) => {
  const lefts = (sel: string) => page.locator(sel).evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().left)));
  await page.goto(`/${T}/analytics`);
  await expect(page.locator('.section-tab.active')).toHaveText('Analytics');
  const sections = await lefts('.section-tab');
  await page.goto(`/${T}/rules`);
  await expect(page.locator('.section-tab.active')).toHaveText('Rules');
  expect(await lefts('.section-tab')).toEqual(sections);

  await page.goto(`/${T}/reviews/rev-1`);
  await expect(page.locator('.tab.active')).toContainText('Overview');
  const tabs = await lefts('[aria-label="Review sections"] .tab');
  await page.goto(`/${T}/reviews/rev-1/conversation`);
  await expect(page.getByRole('combobox', { name: 'Run details' })).toHaveValue('conversation');
  expect(await lefts('[aria-label="Review sections"] .tab')).toEqual(tabs);
});

test('dark theme renders every page without console errors', async ({ page }) => {
  const errors: string[] = [];
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text());
  });
  page.on('pageerror', (e) => errors.push(e.message));
  await page.addInitScript(() => localStorage.setItem('kritika-theme', 'dark'));
  for (const h of [T, `${T}/repos/alpha/one`, `${T}/pulls`, `${T}/reviews/rev-1/diff`, `${T}/reviews/rev-1/conversation`, `${T}/reviews/rev-1/timeline`, `${T}/usage`]) {
    await page.goto(`/${h}`);
    await expect(page.locator('.state-msg[aria-live]')).toHaveCount(0);
    await page.screenshot({ fullPage: true });
  }
  expect(await page.evaluate(() => document.documentElement.className)).toBe('dark');
  expect(errors).toEqual([]);
});

test.describe('pulls load more', () => {
  test('a Load more still in flight when the query changes is discarded', async ({ page }) => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    await g.mockApi(page, g.defaultApi());
    let held = false;
    await page.route((u) => u.pathname.endsWith('/pulls') && u.searchParams.has('cursor'), async (route) => {
      held = true;
      await gate;
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(g.pageOf([{ ...g.pull, number: 8, title: 'More widgets' }])) });
    });
    await page.goto(`/${T}/pulls`);
    await expect(page.locator('.pull-rows .pull-row')).toHaveCount(1);
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect.poll(() => held).toBe(true);
    await page.getByRole('radio', { name: 'All' }).click();
    await expect(page.locator('.pull-rows .pull-row')).toHaveCount(1);
    release();
    await page.waitForTimeout(300);
    await expect(page.locator('.pull-rows')).not.toContainText('More widgets');
    await expect(page.locator('.pull-rows .pull-row')).toHaveCount(1);
  });

  test('a live refetch keeps the pages already loaded', async ({ page }) => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const seen = await g.mockApi(page, g.defaultApi());
    await page.route('**/api/events', async (route) => {
      await gate;
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: `event: review\ndata: ${JSON.stringify({ ...g.liveEvent, account: g.SLUG })}\n\n`,
      });
    });
    await page.goto(`/${T}/pulls`);
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.locator('.pull-rows .pull-row')).toHaveCount(2);
    const firstPages = () => seen.filter((u) => u.pathname.endsWith('/pulls') && !u.searchParams.has('cursor')).length;
    const before = firstPages();
    release();
    await expect.poll(firstPages).toBeGreaterThan(before);
    await expect(page.locator('.pull-rows .pull-row')).toHaveCount(2);
    await expect(page.locator('.pull-rows')).toContainText('More widgets');
  });
});
