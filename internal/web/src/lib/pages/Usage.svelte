<script lang="ts">
  import { accountApi } from '../links';
  import { day } from '../dates';
  import { getJSON } from '../api.svelte';
  import { Resource, live } from '../resource.svelte';
  import { stamp } from '../time.svelte';
  import { callCost, daysAgo, tokens, usd, wholeNumber } from '../format';
  import type { AccountSummary, UsageGroup, UsagePoint, UsageSeries } from '../types';
  import StateView from '../components/StateView.svelte';
  import ColumnChart from '../components/ColumnChart.svelte';
  import Segmented from '../components/Segmented.svelte';
  import StatTile from '../components/StatTile.svelte';
  import Meter from '../components/Meter.svelte';
  import SectionTabs from '../components/SectionTabs.svelte';
  import ChatGPTAllowances from '../components/ChatGPTAllowances.svelte';

  let { slug }: { slug: string } = $props();
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
  ];
  const METRICS = [
    { value: 'cost', label: 'API spend' },
    { value: 'tokens', label: 'Tokens' },
  ] as const;

  const BILLING = [{ value: 'all', label: 'All usage' }, { value: 'chatgpt', label: 'ChatGPT plan' }] as const;

  let days = $state<'1' | '7' | '30' | '90'>('30');
  let group = $state<UsageGroup>('day');
  let metric = $state<'cost' | 'tokens'>('cost');
  let billing = $state<'all' | 'chatgpt'>('all');
  const periodLabel = $derived(days === '1' ? '24 hours' : `${days} days`);

  const total = (p: UsagePoint) => p.inputTokens + p.outputTokens;

  // The 24-hour period is the last 24 hours, not a whole day: a date would
  // start it at midnight UTC.
  const res = new Resource(() => {
    const now = Date.now();
    const from = days === '1' ? new Date(now - 86_400_000).toISOString() : daysAgo(Number(days), now);
    const p = new URLSearchParams({ group, from, billing: chatgptEnabled ? billing : 'all' });
    return getJSON<UsageSeries>(`${accountApi(slug)}/usage?${p}`);
  });
  // The month so far against the account's caps.
  const summary = new Resource(() => getJSON<AccountSummary[]>('/api/v1/accounts'));
  const account = $derived(summary.data?.find((t) => t.slug === slug));
  const month = $derived(account?.usage);
  const chatgptEnabled = $derived(account?.chatgptEnabled ?? false);
  $effect(() => {
    void res.load();
  });
  $effect(() => {
    void summary.load();
  });
  $effect(() => live((e) => e.account === slug && e.kind === 'model_call', () => void res.load(), 2000));

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
    }
    return z;
  }

  const groupLabel = (g: UsageGroup) => GROUPS.find((x) => x.value === g)?.label ?? g;
  // A row with no key is usage no model, repository or role was recorded for.
  const named = (key: string) => key || '(none)';
  const timed = (g: UsageGroup) => g === 'hour' || g === 'day' || g === 'week';
  const keyLabel = (key: string, g: UsageGroup, short = false) => g === 'hour' ? stamp(key) : g === 'week' ? `Week of ${day(key, short)}` : g === 'day' ? day(key, short) : named(key);

  function columns(rows: UsagePoint[], g: UsageGroup) {
    return rows.map((r) => ({
      key: r.key,
      label: timed(g) ? keyLabel(r.key, g, true) : (r.key.split('/').pop() ?? r.key),
      title: keyLabel(r.key, g),
      values: [metric === 'cost' ? r.costUsd : total(r)],
    }));
  }
</script>

<svelte:head><title>Usage · {slug} · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <SectionTabs section="analytics" {slug} current="usage" />
    <p class="small muted">API spend excludes subscription fees.</p>
    {#if chatgptEnabled}<ChatGPTAllowances {slug} />{/if}
    {#if month}
      <section class="stats stats-3" aria-label="This month">
        <StatTile
          label="API spend this month"
          value={usd(month.costUsd)}
          sub={month.medianReviewCostUsd === null
            ? 'no review completed this month'
            : `${usd(month.medianReviewCostUsd)} per review (median) · ${usd(month.reviewCostUsd / month.reviews)} mean of ${wholeNumber(month.reviews)}`}
        />
        <div class="stat">
          <span class="stat-label">Tokens this month</span>
          <span class="stat-value" title={wholeNumber(month.tokens)}>{tokens(month.tokens)}</span>
          <span class="stat-sub">{month.tokensPerMonth ? `of ${tokens(month.tokensPerMonth)} cap` : 'no monthly cap'}</span>
          <Meter value={month.tokens} max={month.tokensPerMonth} label="Monthly tokens used" />
        </div>
        <div class="stat">
          <span class="stat-label">Reviews today</span>
          <span class="stat-value">{wholeNumber(month.reviewsToday)}</span>
          <span class="stat-sub">{month.reviewsPerDay ? `of ${month.reviewsPerDay} a day` : 'no daily cap'}</span>
          <Meter value={month.reviewsToday} max={month.reviewsPerDay} label="Reviews today" />
        </div>
      </section>
    {/if}
    <div class="toolbar">
      <Segmented label="Period" options={PERIODS} value={days} onchange={(d) => (days = d)} />
      <Segmented label="Group by" options={GROUPS} value={group} onchange={(g) => { group = g; if (g === 'hour') days = '1'; }} />
      <Segmented label="Chart metric" options={METRICS} value={metric} onchange={(m) => (metric = m)} />
      {#if chatgptEnabled}
        <Segmented label="Billing" options={BILLING} value={billing} onchange={(b) => { billing = b; if (b === 'chatgpt') metric = 'tokens'; }} />
      {/if}
    </div>
    <StateView {res} retry={() => res.load()} isEmpty={(s) => s.rows.length === 0} empty="No model usage in this range.">
      {#snippet children(s)}
        {@const t = sum(s.rows)}
        {#if chatgptEnabled && billing === 'chatgpt'}
          <section class="stats stats-3" aria-label="ChatGPT usage in this period">
            <StatTile label="ChatGPT calls" value={wholeNumber(t.calls)} sub={`Last ${periodLabel}`} />
            <StatTile label="ChatGPT tokens" value={tokens(total(t))} define={wholeNumber(total(t))} sub="Input includes cached tokens" />
            <StatTile label="Billing" value="Included in plan" sub="Allowance consumption is reported separately by OpenAI" />
          </section>
        {/if}
        <section class="panel" aria-labelledby="usage-chart">
          <header class="panel-head"><h2 id="usage-chart">{metric === 'cost' ? 'API spend' : 'Tokens'} by {groupLabel(s.group).toLowerCase()}</h2></header>
          <ColumnChart
            label="{metric === 'cost' ? 'API spend' : 'Tokens'} by {s.group}, last {periodLabel}"
            series={[{ label: metric === 'cost' ? 'API spend' : 'Tokens', color: 'var(--chart-ink)' }]}
            rows={columns(s.rows, s.group)}
            format={metric === 'cost' ? usd : tokens}
            whole={metric !== 'cost'}
          />
        </section>
        <div class="table-wrap table-card">
          <table class="data">
            <thead>
              <tr>
                <th scope="col">{groupLabel(s.group)}</th><th scope="col" class="num">Calls</th><th scope="col" class="num">Input</th>
                <th scope="col" class="num">Cache read</th><th scope="col" class="num">Cache write</th><th scope="col" class="num">Output</th>
                <th scope="col">Billing</th><th scope="col" class="num">API spend</th>
              </tr>
            </thead>
            <tbody>
              <!-- Time groups read newest first. -->
              {#each timed(s.group) ? [...s.rows].reverse() : s.rows as r (r.key)}
                <tr>
                  <td class:mono={s.group === 'model' || s.group === 'repo'} class="small">{keyLabel(r.key, s.group)}</td>
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
                  <td class="num">{callCost(r.costUsd, r.planCalls === r.calls && r.calls > 0)}</td>
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
                <td class="num">{usd(t.costUsd)}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      {/snippet}
    </StateView>
  </div>
</main>
