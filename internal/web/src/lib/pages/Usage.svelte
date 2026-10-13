<script lang="ts">
  import { usageFilter, type UsageFilter } from '../routes';
  import { href, navigate } from '../router.svelte';
  import { accountApi } from '../links';
  import { day } from '../dates';
  import { getJSON } from '../api.svelte';
  import { Resource, live } from '../resource.svelte';
  import { stamp } from '../time.svelte';
  import { change, callCost, costTotal, daysAgo, reviewsLeftOut, tokens, usd, wholeNumber } from '../format';
  import type { AccountDetail, AccountSummary, MonthUsage, UsageGroup, UsagePoint, UsageSeries } from '../types';
  import StateView from '../components/StateView.svelte';
  import ColumnChart from '../components/ColumnChart.svelte';
  import Segmented from '../components/Segmented.svelte';
  import StatTile from '../components/StatTile.svelte';
  import Meter from '../components/Meter.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import HelpPopover from '../components/HelpPopover.svelte';
  import ChatGPTAllowances from '../components/ChatGPTAllowances.svelte';

  let { slug, filter }: { slug: string; filter?: UsageFilter } = $props();
  function setFilter(patch: UsageFilter) {
    navigate({ name: 'usage', slug, filter: usageFilter({ ...filter, ...patch }) });
  }
  const PERIODS = [
    { value: '1', label: '24 hours' },
    { value: '7', label: '7 days' },
    { value: '30', label: '30 days' },
    { value: '90', label: '90 days' },
  ] as const;
  const GROUPS: readonly { value: UsageGroup; label: string }[] = [
    { value: 'hour', label: 'Hour' },
    { value: 'day', label: 'Day' },
    { value: 'week', label: 'Week' },
    { value: 'model', label: 'Model' },
    { value: 'repo', label: 'Repository' },
    { value: 'role', label: 'Role' },
    { value: 'review', label: 'Review run' },
  ];
  const METRICS = [
    { value: 'cost', label: 'API spend' },
    { value: 'tokens', label: 'Tokens' },
  ] as const;

  const BILLING = [{ value: 'all', label: 'All usage' }, { value: 'chatgpt', label: 'ChatGPT plan' }] as const;

  const days = $derived(filter?.days ?? '30');
  const group = $derived(filter?.group ?? 'day');
  const repository = $derived(filter?.repo);
  const model = $derived(filter?.model);
  const metric = $derived(filter?.metric ?? 'cost');
  const billing = $derived(filter?.billing ?? 'all');
  const periodLabel = $derived(days === '1' ? '24 hours' : `${days} days`);

  const total = (p: UsagePoint) => p.inputTokens + p.outputTokens;
  const value = (p: UsagePoint) => metric === 'cost' ? p.costUsd : total(p);

  // The 24-hour period is the last 24 hours, not a whole day: a date would
  // start it at midnight UTC.
  const res = new Resource(() => {
    const now = Date.now();
    const from = days === '1' ? new Date(now - 86_400_000).toISOString() : daysAgo(Number(days), now);
    const p = new URLSearchParams({ group, from, billing: chatgptEnabled ? billing : 'all' });
    if (group === 'review') p.set('metric', metric);
    if (repository !== undefined) p.set('repo', repository);
    if (model !== undefined) p.set('model', model);
    return getJSON<UsageSeries>(`${accountApi(slug)}/usage?${p}`);
  }, () => JSON.stringify([group, days, chatgptEnabled ? billing : 'all', repository, model, group === 'review' ? metric : null]));
  // The month so far against the account's caps.
  const summary = new Resource(() => getJSON<AccountSummary[]>('/api/v1/accounts'));
  const account = $derived(summary.data?.find((t) => t.slug === slug));
  const detail = new Resource(() => getJSON<AccountDetail>(accountApi(slug)));
  const month = $derived(detail.data?.usage ?? account?.usage);

  // The median and the mean are of the reviews whose cost is known.
  function reviewSpend(m: MonthUsage): string {
    const priced = m.reviews - (m.unpricedReviews ?? 0);
    if (!m.reviews) return 'no review completed this month';
    if (!priced || m.medianReviewCostUsd === null) return `${wholeNumber(m.reviews)} reviews this month, all with unpriced calls`;
    return `${usd(m.medianReviewCostUsd)} per review (median) · ${usd(m.reviewCostUsd / priced)} mean of ${wholeNumber(priced)}${reviewsLeftOut(m.unpricedReviews)}`;
  }
  const chatgptEnabled = $derived(account?.chatgptEnabled ?? false);
  $effect(() => {
    void res.load();
  });
  $effect(() => {
    void summary.load();
  });
  $effect(() =>
    live(
      (e) => e.account === slug && (e.kind === 'model_call' || e.kind === 'review'),
      (dirty) => {
        if (dirty.has('model_call') || dirty.has('resync')) void res.load();
        void detail.load();
      },
      2000,
    ),
  );

  function sum(rows: UsagePoint[]): UsagePoint {
    const z: UsagePoint = { key: 'Total', inputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, outputTokens: 0, costUsd: 0, calls: 0 };
    for (const r of rows) {
      z.inputTokens += r.inputTokens;
      z.cacheReadTokens += r.cacheReadTokens;
      z.cacheWriteTokens += r.cacheWriteTokens;
      z.outputTokens += r.outputTokens;
      z.costUsd += r.costUsd;
      z.calls += r.calls;
      z.planCalls = (z.planCalls ?? 0) + (r.planCalls ?? 0);
      z.unpricedCalls = (z.unpricedCalls ?? 0) + (r.unpricedCalls ?? 0);
    }
    return z;
  }

  const groupLabel = (g: UsageGroup) => GROUPS.find((x) => x.value === g)?.label ?? g;
  // A row with no key is usage no model, repository or role was recorded for.
  const named = (key: string) => key || '(none)';
  const timed = (g: UsageGroup) => g === 'hour' || g === 'day' || g === 'week';
  const keyLabel = (key: string, g: UsageGroup, short = false) => g === 'hour' ? stamp(key) : g === 'week' ? `Week of ${day(key, short)}` : g === 'day' ? day(key, short) : named(key);

  function drill(key: string, by: UsageGroup) {
    if (by === 'repo') setFilter({ repo: key, model: undefined, group: 'model' });
    if (by === 'model') setFilter({ model: key, group: 'review' });
  }

  function tableRows(s: UsageSeries): UsagePoint[] {
    if (timed(s.group)) return [...s.rows].reverse();
    const rows = new Map(s.rows.map((r) => [r.key, r]));
    for (const r of s.group === 'review' ? [] : s.previous) {
      if (!rows.has(r.key)) rows.set(r.key, { ...sum([]), key: r.key });
    }
    return [...rows.values()].sort((a, b) => value(b) - value(a) || a.key.localeCompare(b.key));
  }

  function columns(rows: UsagePoint[], g: UsageGroup) {
    return rows.map((r) => ({
      key: r.key,
      label: g === 'review' ? (r.key === '__other__' ? 'Other reviews' : r.key.slice(0, 8) || 'Unattributed') : timed(g) ? keyLabel(r.key, g, true) : (r.key.split('/').pop() ?? r.key),
      title: keyLabel(r.key, g),
      values: [value(r)],
    }));
  }
</script>

<svelte:head><title>Usage & costs · {slug} · kritika</title></svelte:head>

<main class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Usage & costs" {slug}>
      {#snippet actions()}<HelpPopover label="About usage">
        <p>Changes compare the selection with the preceding period of equal length. API spend excludes subscription fees. Cache detail is available only while transcripts are retained.</p>
        <p>Follow-ups and usage without an associated review are listed as unattributed. Review groups show up to 50 runs with the most usage by the selected metric; Other reviews includes the remainder. Totals include all usage.</p>
        {#if month}<p>This month: {reviewSpend(month)}.</p>{/if}
      </HelpPopover>{/snippet}
    </PageHeader>
    {#snippet limits()}
      {#if month}
        <section class="limits-strip grid grid-cols-[repeat(3,_minmax(0,_1fr))] gap-6 py-3.5 px-4 bg-surface rounded-md" aria-label="This month">
          <div><span class="small muted text-ui-sm">API spend this month</span><strong>{costTotal(month.costUsd, month.unpricedCalls)}</strong></div>
          <div><span class="small muted text-ui-sm">Tokens this month</span><strong title={wholeNumber(month.tokens)}>{tokens(month.tokens)} <span class="small muted text-ui-sm">{month.tokensPerMonth ? `of ${tokens(month.tokensPerMonth)} cap` : 'no cap'}</span></strong><Meter value={month.tokens} max={month.tokensPerMonth} label="Monthly tokens used" /></div>
          <div><span class="small muted text-ui-sm">Reviews today</span><strong>{wholeNumber(month.reviewsToday)} <span class="small muted text-ui-sm">{month.reviewsPerDay ? `of ${month.reviewsPerDay} daily cap` : 'no cap'}</span></strong><Meter value={month.reviewsToday} max={month.reviewsPerDay} label="Reviews today" /></div>
        </section>
      {/if}
    {/snippet}
    <div class="toolbar flex flex-wrap items-center gap-2">
      <Segmented label="Period" options={PERIODS} value={days} onchange={(days) => setFilter({ days })} />
      <label class="filter-select flex items-center gap-2 text-muted text-ui-sm">Group by
        <select aria-label="Group by" value={group} onchange={(e) => { const group = e.currentTarget.value as UsageGroup; setFilter({ group, days: group === 'hour' ? '1' : days }); }}>
          {#each GROUPS as option (option.value)}<option value={option.value}>{option.label}</option>{/each}
        </select>
      </label>
      <Segmented label="Chart metric" options={METRICS} value={metric} onchange={(metric) => setFilter({ metric })} />
      {#if chatgptEnabled}
        <Segmented label="Billing" options={BILLING} value={billing} onchange={(billing) => setFilter({ billing, metric: billing === 'chatgpt' ? 'tokens' : metric })} />
      {/if}
    </div>
    <nav class="usage-drilldown flex items-center flex-wrap my-4 mx-0 text-ui-sm gap-2" aria-label="Spending drilldown">
      <button class="btn border border-solid border-line bg-surface text-ink rounded-md py-1.5 px-3 text-ui-md cursor-pointer inline-flex items-center gap-1.5" onclick={() => setFilter({ repo: undefined, model: undefined, group: 'repo' })}>All repositories</button>
      {#if repository !== undefined}<span>→</span><button class="btn border border-solid border-line bg-surface text-ink rounded-md py-1.5 px-3 text-ui-md cursor-pointer inline-flex items-center gap-1.5" onclick={() => setFilter({ model: undefined, group: 'model' })}>{repository || 'Unattributed repository'}</button>{/if}
      {#if model !== undefined}<span>→</span><span class="badge mono inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium">{model || 'Unattributed model'}</span>{/if}
      {#if repository !== undefined || model !== undefined}<button class="btn border border-solid border-line bg-surface text-ink rounded-md py-1.5 px-3 text-ui-md cursor-pointer inline-flex items-center gap-1.5" onclick={() => setFilter({ repo: undefined, model: undefined })}>Clear filters</button>{/if}
    </nav>
    <StateView {res} retry={() => res.load()} isEmpty={(s) => s.rows.length === 0 && s.previous.length === 0} empty="No model usage in this range.">
      {#snippet children(s)}
        {@const t = sum(s.rows)}
        {@const before = sum(s.previous)}
        {@const previous = new Map(s.previous.map((r) => [r.key, r]))}
        {#if t.unpricedCalls}<p class="small muted text-ui-sm">API spend is incomplete: {wholeNumber(t.unpricedCalls)} unpriced calls. Charts show priced spend only.</p>{/if}
        {#if chatgptEnabled && billing === 'chatgpt'}<p class="small muted text-ui-sm" role="note">ChatGPT plan usage is included in the subscription. Allowance consumption is reported separately by OpenAI.</p>{/if}
        <section class="stats stats-3 grid grid-cols-[repeat(auto-fit,_minmax(160px,_1fr))] gap-5 bg-transparent border-0 rounded-[0] overflow-hidden" aria-label="Selected usage">
          <StatTile label="API spend in selection" value={costTotal(t.costUsd, t.unpricedCalls)} now={t.costUsd} before={t.unpricedCalls || before.unpricedCalls ? undefined : before.costUsd} sub={`Last ${periodLabel} vs preceding period`} />
          <StatTile label="Tokens in selection" value={tokens(total(t))} now={total(t)} before={total(before)} define={wholeNumber(total(t))} />
          <StatTile label="Calls in selection" value={wholeNumber(t.calls)} now={t.calls} before={before.calls} />
        </section>

        <section class="panel bg-panel border border-solid border-line rounded-lg min-w-0" aria-labelledby="usage-chart">
          <header class="panel-head flex items-center justify-between border-b border-solid border-b-line py-3 px-4 gap-3"><h2 id="usage-chart">{metric === 'cost' ? 'API spend' : 'Tokens'} by {groupLabel(s.group).toLowerCase()}</h2></header>
          <ColumnChart
            label="{metric === 'cost' ? 'API spend' : 'Tokens'} by {s.group}, last {periodLabel}"
            series={[{ label: metric === 'cost' ? 'API spend' : 'Tokens', color: 'var(--chart-ink)' }]}
            rows={columns(s.rows, s.group)}
            format={metric === 'cost' ? usd : tokens}
            whole={metric !== 'cost'}
          />
        </section>
        {@render limits()}
        <div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
          <table class="data">
            <thead>
              <tr>
                <th scope="col">{groupLabel(s.group)}</th><th scope="col" class="num">Calls</th><th scope="col" class="num">Input</th>
                <th scope="col" class="num">Cache read</th><th scope="col" class="num">Cache write</th><th scope="col" class="num">Output</th>
                <th scope="col">Billing</th><th scope="col" class="num">API spend</th>
                {#if !timed(s.group) && s.group !== 'review'}<th scope="col" class="num" title={`${metric === 'cost' ? 'API spend' : 'Tokens'} compared with the preceding period of equal length`}>Change</th>{/if}
              </tr>
            </thead>
            <tbody>
              <!-- Time groups read newest first. -->
              {#each tableRows(s) as r (r.key)}
                {@const prior = previous.get(r.key)}
                <tr>
                  <td class:mono={s.group === 'model' || s.group === 'repo'} class="small text-ui-sm">
                    {#if s.group === 'repo' || s.group === 'model'}
                      <button class="drill-link border-0 bg-transparent text-accent text-left cursor-pointer py-1.5 px-0 wrap-anywhere" onclick={() => drill(r.key, s.group)}>{keyLabel(r.key, s.group)} →</button>
                    {:else if s.group === 'review' && r.key === '__other__'}<span>Other reviews</span>
                    {:else if s.group === 'review' && r.key}
                      <a href={href({ name: 'review', slug, id: r.key, tab: 'usage' })}>Review {r.key.slice(0, 8)} →</a>
                    {:else if s.group === 'review'}<span>Unattributed usage</span>
                    {:else}{keyLabel(r.key, s.group)}{/if}
                  </td>
                  <td class="num">{wholeNumber(r.calls)}</td>
                  <td class="num" title={wholeNumber(r.inputTokens)}>{tokens(r.inputTokens)}</td>
                  <td class="num" title={wholeNumber(r.cacheReadTokens)}>{tokens(r.cacheReadTokens)}</td>
                  <td class="num" title={wholeNumber(r.cacheWriteTokens)}>{tokens(r.cacheWriteTokens)}</td>
                  <td class="num" title={wholeNumber(r.outputTokens)}>{tokens(r.outputTokens)}</td>
                  <td>
                    {#if r.planCalls === r.calls && r.calls > 0}ChatGPT plan
                    {:else if r.planCalls}API + ChatGPT plan
                    {:else}API{/if}
                  </td>
                  <td class="num">{callCost(r.costUsd, r.planCalls === r.calls && r.calls > 0, !!r.unpricedCalls)}</td>
                  {#if !timed(s.group) && s.group !== 'review'}<td class="num" title={prior ? `Previous period: ${metric === 'cost' ? costTotal(prior.costUsd, prior.unpricedCalls) : tokens(total(prior))}` : 'No usage in the previous period'}>{metric === 'cost' && (r.unpricedCalls || prior?.unpricedCalls) ? 'Incomplete' : change(value(r), prior ? value(prior) : 0)?.text ?? '—'}</td>{/if}
                </tr>
              {/each}
            </tbody>
            <tfoot>
              <tr>
                <th scope="row">Total API spend</th>
                <td class="num">{wholeNumber(t.calls)}</td>
                <td class="num" title={wholeNumber(t.inputTokens)}>{tokens(t.inputTokens)}</td>
                <td class="num" title={wholeNumber(t.cacheReadTokens)}>{tokens(t.cacheReadTokens)}</td>
                <td class="num" title={wholeNumber(t.cacheWriteTokens)}>{tokens(t.cacheWriteTokens)}</td>
                <td class="num" title={wholeNumber(t.outputTokens)}>{tokens(t.outputTokens)}</td>
                <td></td>
                <td class="num">{costTotal(t.costUsd, t.unpricedCalls)}</td>
                {#if !timed(s.group) && s.group !== 'review'}<td></td>{/if}
              </tr>
            </tfoot>
          </table>
        </div>
      {/snippet}
    </StateView>
    {#if !res.data || (!res.data.rows.length && !res.data.previous.length)}{@render limits()}{/if}
    {#if chatgptEnabled}<ChatGPTAllowances {slug} />{/if}
  </div>
</main>
