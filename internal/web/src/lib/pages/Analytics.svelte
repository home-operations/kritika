<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { usageFilter, analyticsFilter, type AnalyticsFilter } from '../routes';
  import { href, navigate } from '../router.svelte';
  import { Resource, live } from '../resource.svelte';
  import { accountApi, repoRoute } from '../links';
  import { day, timestamp } from '../dates';
  import { daysAgo, duration, costTotal, wholeNumber, tokens, usd, CATEGORIES } from '../format';
  import type { Analytics, AnalyticsPoint, DependencyDrift, EgressRefusal } from '../types';
  import StateView from '../components/StateView.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import HelpPopover from '../components/HelpPopover.svelte';
  import Segmented from '../components/Segmented.svelte';
  import StatTile from '../components/StatTile.svelte';
  import ColumnChart from '../components/ColumnChart.svelte';
  import Meter from '../components/Meter.svelte';
  import SeverityCounts from '../components/SeverityCounts.svelte';

  // No slug is the instance: every readable account added up, which has
  // no medians.
  let { slug, filter }: { slug?: string; filter?: AnalyticsFilter } = $props();
  const base = $derived(slug ? accountApi(slug) : '/api/v1');
  const analyticsRoute = (filter?: AnalyticsFilter) => slug ? { name: 'analytics' as const, slug, filter } : { name: 'instanceAnalytics' as const, filter };

  const PERIODS = [
    { value: '1', label: '24 hours' },
    { value: '7', label: '7 days' },
    { value: '30', label: '30 days' },
    { value: '90', label: '90 days' },
  ] as const;
  const days = $derived(filter?.days ?? '30');
  const ALL_TRENDS = [
    { value: 'reviews', label: 'Reviews' },
    { value: 'findings', label: 'Findings by severity' },
    { value: 'failed', label: 'Failure rate' },
    { value: 'duration', label: 'Duration' },
    { value: 'tokens', label: 'Tokens' },
    { value: 'cost', label: 'API spend' },
  ] as const;
  type Trend = typeof ALL_TRENDS[number]['value'];
  const TRENDS = $derived(ALL_TRENDS.filter((t) => slug || t.value !== 'duration'));
  let trend = $state<Trend>('reviews');
  let view = $state<'chart' | 'table'>('chart');
  const trendLabel = $derived(TRENDS.find((t) => t.value === trend)!.label);
  // A quarter reads better by week than as ninety thin columns.
  const group = $derived(days === '90' ? 'week' : 'day');
  const bucketLabel = $derived(days === '1' ? 'partial day' : group);

  const res = new Resource((signal) => {
    const now = Date.now();
    const from = days === '1' ? new Date(now - 86_400_000).toISOString() : daysAgo(Number(days), now);
    const p = new URLSearchParams({ from, group });
    return getJSON<Analytics>(`${base}/analytics?${p}`, signal);
  }, () => JSON.stringify([base, 'analytics', days, group]));
  $effect(() => { void res.load(); });
  $effect(() => live((e) => (!slug || e.account === slug) && e.kind === 'review', () => void res.load(), 1000));

  // The dependencies an account's pull requests moved and how each update
  // was rated: an account's own, since the judge rates under its
  // instructions.
  const drift = new Resource((signal) => {
    const now = Date.now();
    const from = days === '1' ? new Date(now - 86_400_000).toISOString() : daysAgo(Number(days), now);
    return getJSON<DependencyDrift[]>(`${base}/analytics/dependencies?${new URLSearchParams({ from })}`, signal);
  }, () => JSON.stringify([base, 'dependencies', days]));
  $effect(() => { if (slug) void drift.load(); });
  $effect(() => live((e) => e.account === slug && e.kind === 'review', () => { if (slug) void drift.load(); }, 1000));
  const LEVELS = ['low', 'medium', 'high', 'critical'];
  const REACHES = ['none', 'standalone', 'shared', 'central'];
  const counted = (counts: Record<string, number>, order: string[]) =>
    order.filter((k) => counts[k]).map((k) => `${k} ${wholeNumber(counts[k]!)}`).join(' · ');

  const found = (c: { p0: number; p1: number; p2: number }) => c.p0 + c.p1 + c.p2;
  const rate = (addressed: number, total: number) => (total ? (addressed / total) * 100 : null);

  function rows(series: AnalyticsPoint[], values: (p: AnalyticsPoint) => number[]) {
    return series.map((p) => ({
      key: p.key,
      label: day(p.key, true),
      title: group === 'week' ? `Week of ${day(p.key)}` : `${day(p.key)}${days === '1' ? ' (partial day)' : ''}`,
      values: values(p),
    }));
  }

  const VIEWS = [
    { value: 'chart', label: 'Chart' },
    { value: 'table', label: 'Table' },
  ] as const;
  const SEV_SERIES = [
    { label: 'P0', color: 'var(--chart-sev-p0)' },
    { label: 'P1', color: 'var(--chart-sev-p1)' },
    { label: 'P2', color: 'var(--chart-sev-p2)' },
  ];

  function values(point: AnalyticsPoint): number[] {
    switch (trend) {
      case 'reviews': return [point.reviews];
      case 'findings': return [point.findings.p0, point.findings.p1, point.findings.p2];
      case 'failed': return [rate(point.failed, point.reviews + point.failed)!];
      case 'duration': return [point.medianReviewMs!];
      case 'tokens': return [point.tokens];
      case 'cost': return [point.costUsd];
    }
  }

  // The refused hosts of one repository, or of all, added up per host with
  // the reasons each was refused for, the most refused first.
  let refusedRepo = $state('');
  const refusedRepos = (rows: EgressRefusal[]) => [...new Set(rows.map((r) => r.repository).filter(Boolean))].sort();
  function refusedHosts(rows: EgressRefusal[]) {
    const hosts = new Map<string, { host: string; count: number; runs: number; reasons: Map<string, number> }>();
    for (const r of rows) {
      if (refusedRepo && r.repository !== refusedRepo) continue;
      const h = hosts.get(r.host) ?? { host: r.host, count: 0, runs: 0, reasons: new Map() };
      h.count += r.count;
      h.runs += r.runs;
      for (const [reason, count] of Object.entries(r.reasons)) h.reasons.set(reason, (h.reasons.get(reason) ?? 0) + count);
      hosts.set(r.host, h);
    }
    return [...hosts.values()].sort((a, b) => b.count - a.count || a.host.localeCompare(b.host)).map((h) => ({
      ...h,
      reasons: [...h.reasons].sort((a, b) => b[1] - a[1]).map(([reason, count]) => `${reason} ${wholeNumber(count)}`).join(' · '),
    }));
  }

  function formatValue(value: number): string {
    switch (trend) {
      case 'failed': return `${value.toFixed(1)}%`;
      case 'duration': return duration(value);
      case 'tokens': return tokens(value);
      case 'cost': return usd(value);
      default: return wholeNumber(value);
    }
  }
</script>

<svelte:head><title>Analytics · {slug ?? 'All accounts'} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner @container w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Analytics" {slug}>
      {#snippet actions()}<a class="btn" href={href(slug ? { name: 'usage', slug, filter: usageFilter({ days }) } : { name: 'instanceUsage', filter: usageFilter({ days }) })}>Investigate spending →</a><HelpPopover label="About these metrics"><p>Changes compare with the preceding period of equal length. Review counts use start dates; spend and tokens use call dates.</p><p>Failure rate is failed / (completed + failed). {#if slug}Duration is the median of completed reviews. {:else}Medians are each account's own, so there are none here. {/if}API spend includes priced calls and excludes subscription fees.</p></HelpPopover>{/snippet}
    </PageHeader>
    <div class="toolbar flex flex-wrap items-center gap-2">
      <Segmented label="Period" options={PERIODS} value={days} onchange={(days) => navigate(analyticsRoute(analyticsFilter({ days })))} />
    </div>
    <StateView {res} retry={() => res.load()}>
      {#snippet children(d)}
        {@const c = d.current}
        {@const p = d.previous}
        <div class="analytics flex flex-col gap-3.5 [transition:opacity_0.15s]" class:stale={res.loading}>
          <section class="stats" class:stats-3={slug} class:stats-4={!slug} aria-label="Totals">
            <StatTile
              label="Reviews"
              value={wholeNumber(c.reviews)}
              sub={[c.failed ? `${c.failed} failed` : '', `on ${wholeNumber(c.pullRequests)} pull ${c.pullRequests === 1 ? 'request' : 'requests'}`].filter(Boolean).join(' · ')}
              now={c.reviews}
              before={p.reviews}
              define="Completed reviews in the period, and the pull requests they were of"
            />
            {#if slug}
              <StatTile
                label="Median review"
                value={c.medianReviewMs === null ? '—' : duration(c.medianReviewMs)}
                now={c.medianReviewMs}
                before={p.medianReviewMs}
                good="down"
                define="How long a completed review took, from start to finish"
              />
              <StatTile
                label="Time to merge"
                value={c.medianMergeMs === null ? '—' : duration(c.medianMergeMs)}
                now={c.medianMergeMs}
                before={p.medianMergeMs}
                good="down"
                define="The median time from opening to merging, of the pull requests kritika knows that merged in the period"
              />
            {/if}
            <StatTile
              label="Addressed"
              value={rate(c.addressed, found(c.findings)) === null ? '—' : `${Math.round(rate(c.addressed, found(c.findings))!)}%`}
              sub={`${c.addressed} of ${found(c.findings)} findings · ${c.findings.p0} P0`}
              now={rate(c.addressed, found(c.findings))}
              before={rate(p.addressed, found(p.findings))}
              good="up"
              points
              define="Of the findings first reported in the period, each counted once per pull request, the share a later review of the pull request, at a newer head, no longer reported"
            />
            <StatTile
              label="Reactions"
              value={`${wholeNumber(c.reactionsUp)} up`}
              sub={`${wholeNumber(c.reactionsDown)} down`}
              now={c.reactionsUp - c.reactionsDown}
              before={p.reactionsUp - p.reactionsDown}
              good="up"
              define="The thumbs up and down on the inline comments of the findings reported in the period"
            />
            <StatTile
              label="API spend"
              value={costTotal(c.costUsd, c.unpricedCalls)}
              now={c.costUsd}
              before={c.unpricedCalls || p.unpricedCalls ? undefined : p.costUsd}
              good="down"
              define="API spend in the period, subscription fees excluded, and the input (including cached input) plus output tokens behind it"
              sub={[c.unpricedCalls ? `${wholeNumber(c.unpricedCalls)} unpriced calls; spend is incomplete` : '', `${tokens(c.tokens)} tokens`].filter(Boolean).join(' · ')}
            />
          </section>
          <section class="panel" aria-labelledby="an-trends">
            <header class="panel-head">
              <h2 id="an-trends">Review trends</h2>
              <label class="filter-select flex items-center gap-2 text-muted text-ui-sm">Metric
                <select aria-label="Trend metric" bind:value={trend}>{#each TRENDS as option (option.value)}<option value={option.value}>{option.label}</option>{/each}</select>
              </label>
              <Segmented label="Trend view" options={VIEWS} value={view} onchange={(v) => (view = v)} />
            </header>
            <ColumnChart
              label={trend === 'reviews' ? `Completed reviews per ${bucketLabel}` : trend === 'findings' ? `Findings first reported per ${bucketLabel}, by severity` : `${trendLabel} per ${bucketLabel}`}
              series={trend === 'findings' ? SEV_SERIES : [{ label: trendLabel, color: 'var(--chart-ink)' }]}
              rows={rows(d.series.filter((x) => trend === 'duration' ? x.medianReviewMs !== null : trend === 'failed' ? x.reviews + x.failed > 0 : true), values)}
              format={formatValue}
              whole={trend === 'reviews' || trend === 'findings' || trend === 'tokens'} {view} />
            {#if trend === 'findings'}
              <div class="panel-foot flex flex-wrap items-center gap-2 py-2.5 px-4 border-t border-solid border-t-line text-ui-sm">
                {#each CATEGORIES.filter((k) => c.categories[k] > 0) as k (k)}
                  {#if slug}
                    <a class="badge inline-flex items-center gap-1 text-ui-xs border border-solid border-line py-0 px-[7px] text-muted rounded-sm" href={href({ name: 'findings', slug, filter: { category: k } })}>{k} {wholeNumber(c.categories[k])}</a>
                  {:else}
                    <span class="badge inline-flex items-center gap-1 text-ui-xs border border-solid border-line py-0 px-[7px] text-muted rounded-sm">{k} {wholeNumber(c.categories[k])}</span>
                  {/if}
                {/each}
                {#if slug}<a class="ml-auto" href={href({ name: 'findings', slug })}>See every finding →</a>{/if}
              </div>
            {:else if trend !== 'reviews'}
              <p class="panel-foot small muted m-0 py-2.5 px-4 border-t border-solid border-t-line text-ui-sm">{trend === 'failed' ? 'Failed / (completed + failed). Periods with no outcomes are omitted.' : trend === 'duration' ? 'Median duration of completed reviews. Periods with no completed reviews are omitted.' : trend === 'cost' ? 'Priced API spend only; subscription fees excluded.' : 'Input (including cached input) plus output tokens.'}</p>
            {/if}
          </section>

          <section class="panel" aria-labelledby="an-repos">
            <header class="panel-head"><h2 id="an-repos">Most reviewed repositories</h2></header>
            {#if d.repositories.length === 0}
              <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No reviews in this period.</p>
            {:else}
              <div class="table-wrap overflow-x-auto max-w-full">
                <table class="data">
                  <thead>
                    <tr>
                      <th scope="col">Repository</th>
                      {#if !slug}<th scope="col">Account</th>{/if}
                      <th scope="col" class="num">Reviews</th>
                      <th scope="col">Findings</th>
                      <th scope="col" class="num">Addressed</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each d.repositories as r (r.repository)}
                      {@const n = found(r.findings)}
                      <tr>
                        <td class="mono"><a href={href(repoRoute(r.account ?? slug!, r.repository))}>{r.repository}</a></td>
                        {#if !slug}<td class="mono"><a href={href({ name: 'account', slug: r.account! })}>{r.account}</a></td>{/if}
                        <td class="num">{wholeNumber(r.reviews)}</td>
                        <td><SeverityCounts counts={r.findings} /></td>
                        <td class="num">{n ? `${Math.round((r.addressed / n) * 100)}%` : '—'}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </section>

          <section class="panel" aria-labelledby="an-refused">
            <header class="panel-head">
              <h2 id="an-refused">Refused hosts</h2>
              {#if refusedRepos(d.refusals).length > 0}
                <label class="filter-select flex items-center gap-2 text-muted text-ui-sm">Repository
                  <select aria-label="Refused hosts repository" bind:value={refusedRepo}>
                    <option value="">All repositories</option>
                    {#each refusedRepos(d.refusals) as repo (repo)}<option value={repo}>{repo}</option>{/each}
                  </select>
                </label>
              {/if}
            </header>
            {#if d.refusals.length === 0}
              <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No refused requests in this period.</p>
            {:else}
              {@const hosts = refusedHosts(d.refusals)}
              {@const most = hosts[0]?.count ?? 0}
              <div class="table-wrap overflow-x-auto max-w-full">
                <table class="data refused-hosts">
                  <thead>
                    <tr>
                      <th scope="col">Host</th>
                      <th scope="col" class="num">Refusals</th>
                      <th scope="col" class="num">Runs</th>
                      <th scope="col">Reasons</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each hosts as h (h.host)}
                      <tr>
                        <td class="mono name-fill" title={h.host}>{h.host}</td>
                        <td class="num">
                          {wholeNumber(h.count)}
                          <Meter value={h.count} max={most} label="Share of the most refused host" color="var(--chart-ink)" />
                        </td>
                        <td class="num">{wholeNumber(h.runs)}</td>
                        <td class="small text-ui-sm">{h.reasons}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
              <p class="panel-foot small muted m-0 py-2.5 px-4 border-t border-solid border-t-line text-ui-sm">Hosts the gateway refused a run's commands and fetches, as the agent asked for them. A host a review needs goes in the configuration's egress.allow.</p>
            {/if}
          </section>

          {#if slug}
            <section class="panel" aria-labelledby="an-deps">
              <header class="panel-head"><h2 id="an-deps">Dependency ratings</h2></header>
              <StateView res={drift} retry={() => drift.load()}>
                {#snippet children(deps)}
                  {#if deps.length === 0}
                    <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No dependency was updated more than once in this period.</p>
                  {:else}
                    <div class="table-wrap overflow-x-auto max-w-full">
                      <table class="data dependency-ratings">
                        <thead>
                          <tr>
                            <th scope="col">Repository</th>
                            <th scope="col">Dependency</th>
                            <th scope="col" class="num">Updates</th>
                            <th scope="col">Risks</th>
                            <th scope="col">Reaches</th>
                            <th scope="col">Last</th>
                          </tr>
                        </thead>
                        <tbody>
                          {#each deps as d (`${d.repository} ${d.path} ${d.dependency}`)}
                            <tr class:drifted={d.levels > 1}>
                              <td class="mono"><a href={href(repoRoute(slug, d.repository))}>{d.repository}</a></td>
                              <td class="mono name-fill" title={`${d.path} ${d.dependency}`}>{d.dependency}<span class="block small muted text-ui-sm">{d.path}</span></td>
                              <td class="num">{wholeNumber(d.bumps)}</td>
                              <td class="small text-ui-sm">{counted(d.risks, LEVELS)}</td>
                              <td class="small text-ui-sm">{counted(d.reaches, REACHES)}</td>
                              <td class="small text-ui-sm"><time datetime={d.lastAt}>{timestamp(d.lastAt)}</time>{` · ${d.lastStep}${d.lastRisk ? ` · ${d.lastRisk}` : ''}`}</td>
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                    <p class="panel-foot small muted m-0 py-2.5 px-4 border-t border-solid border-t-line text-ui-sm">The risk the scorer gave each update of a dependency, before any floor, where the pull request moved that version alone, and the reach it gave the dependency, from the latest scored review of each pull request. A dependency rated at more than one level drifted; the most drifted come first.</p>
                  {/if}
                {/snippet}
              </StateView>
            </section>
          {/if}
        </div>
      {/snippet}
    </StateView>
  </div>
</main>
