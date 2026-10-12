<script lang="ts">
  // The account's home: what its reviews came to over a window, against the
  // window before, and what needs attention now.
  import { getJSON } from '../api.svelte';
  import { usageFilter, analyticsFilter, type AnalyticsFilter } from '../routes';
  import { href, navigate } from '../router.svelte';
  import { hookURL } from '../session.svelte';
  import { unsignedWebhooks } from '../setup';
  import { Resource, live } from '../resource.svelte';
  import { accountApi, repoRoute } from '../links';
  import { day } from '../dates';
  import { daysAgo, duration, costTotal, wholeNumber, tokens, usd, CATEGORIES } from '../format';
  import type { AccountDetail, Analytics, AnalyticsPoint, Attention } from '../types';
  import { WANTS, nearCaps } from '../attention';
  import StateView from '../components/StateView.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import HelpPopover from '../components/HelpPopover.svelte';
  import Segmented from '../components/Segmented.svelte';
  import StatTile from '../components/StatTile.svelte';
  import ColumnChart from '../components/ColumnChart.svelte';
  import SeverityCounts from '../components/SeverityCounts.svelte';
  import Pill from '../components/Pill.svelte';
  import Time from '../components/Time.svelte';

  let { slug, filter }: { slug: string; filter?: AnalyticsFilter } = $props();
  const base = $derived(accountApi(slug));

  const PERIODS = [
    { value: '1', label: '24 hours' },
    { value: '7', label: '7 days' },
    { value: '30', label: '30 days' },
    { value: '90', label: '90 days' },
  ] as const;
  const days = $derived(filter?.days ?? '30');
  let trend = $state<'failed' | 'duration' | 'tokens' | 'cost'>('failed');
  let operationsView = $state<'chart' | 'table'>('chart');
  const TRENDS = [{ value: 'failed', label: 'Failure rate' }, { value: 'duration', label: 'Duration' }, { value: 'tokens', label: 'Tokens' }, { value: 'cost', label: 'API spend' }] as const;
  // A quarter reads better by week than as ninety thin columns.
  const group = $derived(days === '90' ? 'week' : 'day');
  const bucketLabel = $derived(days === '1' ? 'partial day' : group);

  const res = new Resource(() => {
    const now = Date.now();
    const from = days === '1' ? new Date(now - 86_400_000).toISOString() : daysAgo(Number(days), now);
    const p = new URLSearchParams({ from, group });
    return getJSON<Analytics>(`${base}/analytics?${p}`);
  });
  const detail = new Resource(() => getJSON<AccountDetail>(base));
  const attention = new Resource(() => getJSON<Attention>(`${base}/attention`));
  $effect(() => {
    void res.load();
  });
  $effect(() => {
    void detail.load();
    void attention.load();
  });
  $effect(() =>
    live(
      (e) => e.account === slug && e.kind === 'review',
      () => {
        void res.load();
        void attention.load();
        void detail.load();
      },
      1000,
    ),
  );

  const plural = (n: number) => `${wholeNumber(n)} open ${n === 1 ? 'pull request' : 'pull requests'}`;

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

  let reviewsView = $state<'chart' | 'table'>('chart');
  let findingsView = $state<'chart' | 'table'>('chart');
  const VIEWS = [
    { value: 'chart', label: 'Chart' },
    { value: 'table', label: 'Table' },
  ] as const;
  const SEV_SERIES = [
    { label: 'P0', color: 'var(--chart-sev-p0)' },
    { label: 'P1', color: 'var(--chart-sev-p1)' },
    { label: 'P2', color: 'var(--chart-sev-p2)' },
  ];
</script>

<svelte:head><title>Analytics · {slug} · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <PageHeader title="Analytics" {slug}>
      {#snippet actions()}<HelpPopover label="About these metrics"><p>Changes compare with the preceding period of equal length. Review counts use start dates; spend and tokens use call dates.</p><p>Failure rate is failed / (completed + failed). Duration is the median of completed reviews. API spend includes priced calls and excludes subscription fees.</p></HelpPopover>{/snippet}
    </PageHeader>
    <div class="toolbar">
      <Segmented label="Period" options={PERIODS} value={days} onchange={(days) => navigate({ name: 'account', slug, filter: analyticsFilter({ days }) })} />
    </div>
    {#if detail.data && (unsignedWebhooks(detail.data.connection) || !detail.data.connection.lastWebhookAt)}
      {@const inst = detail.data.connection}
      <section class="panel" aria-labelledby="an-connection">
        <header class="panel-head"><h2 id="an-connection">Connection</h2></header>
        {#if unsignedWebhooks(inst)}
          <p class="notice" role="note">
            GitHub sends <span class="mono">{inst.name}</span>'s webhooks with no signature, so kritika refuses them, only polls for
            new pull requests and cannot answer mentions. Set the GitHub App's webhook secret to the one kritika holds.
          </p>
        {:else}
          <p class="notice" role="note">
            No webhook has reached <span class="mono">{inst.name}</span>, so kritika only polls it for new pull requests and
            cannot answer mentions. Point the GitHub App's webhook at <span class="mono">{hookURL(inst.hookPath)}</span>.
          </p>
        {/if}
        <p class="small muted panel-body last-poll">
          {#if detail.data.lastPolledAt}Last polled <Time iso={detail.data.lastPolledAt} />.{:else}Not polled yet.{/if}
        </p>
      </section>
    {/if}

    {#if attention.data && detail.data}
      {@const wants = WANTS.filter((w) => attention.data![w.key] > 0)}
      {@const near = nearCaps(detail.data.usage)}
      {#if wants.length || near.length}
        <section class="panel" aria-labelledby="an-attention">
          <header class="panel-head"><h2 id="an-attention">Needs attention</h2><span class="small muted">Right now</span></header>
          <ul class="rows">
            {#each wants as w (w.key)}
              <li class="row">
                <a class="row-link" href={href({ name: 'pulls', slug, filter: w.filter })}>
                  <Pill tone={w.tone} label={w.label} />
                  <span class="row-text">{plural(attention.data[w.key])} {w.why}</span>
                </a>
              </li>
            {/each}
            {#each near as text (text)}
              <li class="row">
                <a class="row-link" href={href({ name: 'usage', slug })}>
                  <Pill tone="warn" label="cap" />
                  <span class="row-text">{text}</span>
                </a>
              </li>
            {/each}
          </ul>
        </section>
      {/if}
    {/if}

    {#if days === '1'}<p class="small muted">Last 24 hours, split by date. Each chart column includes only the part of that day inside the selected period.</p>{/if}
    <StateView {res} retry={() => res.load()}>
      {#snippet children(d)}
        {@const c = d.current}
        {@const p = d.previous}
        <div class="analytics" class:stale={res.loading}>
          <section class="stats stats-3" aria-label="Operational metrics">
            <StatTile
              label="Reviews"
              value={wholeNumber(c.reviews)}
              sub={c.failed ? `${c.failed} failed` : ''}
              now={c.reviews}
              before={p.reviews}
              define="Completed reviews in the period"
            />
            <StatTile
              label="Median review"
              value={c.medianReviewMs === null ? '—' : duration(c.medianReviewMs)}
              now={c.medianReviewMs}
              before={p.medianReviewMs}
              good="down"
              define="How long a completed review took, from start to finish"
            />
            <StatTile
              label="API spend"
              value={costTotal(c.costUsd, c.unpricedCalls)}
              now={c.costUsd}
              before={c.unpricedCalls || p.unpricedCalls ? undefined : p.costUsd}
              good="down"
              define="API spend in the period; subscription fees excluded"
              sub={c.unpricedCalls ? `${wholeNumber(c.unpricedCalls)} unpriced calls; spend is incomplete` : undefined}
            />
            <StatTile label="Failure rate" value={rate(c.failed, c.reviews + c.failed) === null ? '—' : `${rate(c.failed, c.reviews + c.failed)!.toFixed(1)}%`}
              now={rate(c.failed, c.reviews + c.failed)} before={rate(p.failed, p.reviews + p.failed)} points good="down"
              sub={`${c.failed} failed of ${c.reviews + c.failed} completed or failed reviews`} />
            <StatTile label="Tokens" value={tokens(c.tokens)} now={c.tokens} before={p.tokens} define="Input (including cached input) plus output tokens" />
            <a class="stat" href={href({ name: 'usage', slug, filter: usageFilter({ days }) })}><span class="stat-label">Investigate spending</span><span class="stat-value">{costTotal(c.costUsd, c.unpricedCalls)}</span><span class="stat-sub">Repository → model → review run</span></a>
          </section>
          <section class="panel" aria-labelledby="an-operations">
            <header class="panel-head"><h2 id="an-operations">Operational trends</h2><Segmented label="Operational trend view" options={VIEWS} value={operationsView} onchange={(v) => (operationsView = v)} /></header>
            <div class="toolbar"><Segmented label="Operational metric" options={TRENDS} value={trend} onchange={(v) => (trend = v)} /></div>
            <ColumnChart label={`${TRENDS.find((t) => t.value === trend)!.label} per ${bucketLabel}`}
              series={[{ label: TRENDS.find((t) => t.value === trend)!.label, color: 'var(--chart-ink)' }]}
              rows={rows(d.series.filter((x) => trend === 'duration' ? x.medianReviewMs !== null : trend === 'failed' ? x.reviews + x.failed > 0 : true),
                (x) => [trend === 'failed' ? rate(x.failed, x.reviews + x.failed)! : trend === 'duration' ? x.medianReviewMs! : trend === 'tokens' ? x.tokens : x.costUsd])}
              format={trend === 'failed' ? (n) => `${n.toFixed(1)}%` : trend === 'duration' ? duration : trend === 'tokens' ? tokens : usd}
              whole={trend === 'tokens'} view={operationsView} />
            <p class="panel-foot small muted">{trend === 'failed' ? 'Failed / (completed + failed). Periods with no outcomes are omitted.' : trend === 'duration' ? 'Median duration of completed reviews. Periods with no completed reviews are omitted.' : trend === 'cost' ? 'Priced API spend only; subscription fees excluded.' : 'Input (including cached input) plus output tokens.'}</p>
          </section>
          <section class="stats stats-related" aria-label="Totals">
            <StatTile
              label="Pull requests reviewed"
              value={wholeNumber(c.pullRequests)}
              now={c.pullRequests}
              before={p.pullRequests}
              define="Pull requests with a completed review in the period"
            />
            <StatTile
              label="Findings"
              value={wholeNumber(found(c.findings))}
              sub={`${c.findings.p0} P0`}
              now={found(c.findings)}
              before={found(p.findings)}
              define="Findings first reported in the period, each counted once per pull request"
            />
            <StatTile
              label="Addressed"
              value={rate(c.addressed, found(c.findings)) === null ? '—' : `${Math.round(rate(c.addressed, found(c.findings))!)}%`}
              sub={`${c.addressed} of ${found(c.findings)}`}
              now={rate(c.addressed, found(c.findings))}
              before={rate(p.addressed, found(p.findings))}
              good="up"
              points
              define="Of those findings, the share a later review of the pull request, at a newer head, no longer reported"
            />
            <StatTile
              label="Time to merge"
              value={c.medianMergeMs === null ? '—' : duration(c.medianMergeMs)}
              now={c.medianMergeMs}
              before={p.medianMergeMs}
              good="down"
              define="The median time from opening to merging, of the pull requests kritika knows that merged in the period"
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
          </section>

          <div class="grid-2">
            <section class="panel" aria-labelledby="an-reviews">
              <header class="panel-head">
                <h2 id="an-reviews">Reviews</h2>
                <Segmented label="Reviews view" options={VIEWS} value={reviewsView} onchange={(v) => (reviewsView = v)} />
              </header>
              <ColumnChart
                label="Completed reviews per {bucketLabel}"
                series={[{ label: 'Reviews', color: 'var(--chart-ink)' }]}
                rows={rows(d.series, (x) => [x.reviews])}
                format={wholeNumber}
                whole
                view={reviewsView}
              />
            </section>
            <section class="panel" aria-labelledby="an-findings">
              <header class="panel-head">
                <h2 id="an-findings">Findings by severity</h2>
                <Segmented label="Findings view" options={VIEWS} value={findingsView} onchange={(v) => (findingsView = v)} />
              </header>
              <ColumnChart
                label="Findings first reported per {bucketLabel}, by severity"
                series={SEV_SERIES}
                rows={rows(d.series, (x) => [x.findings.p0, x.findings.p1, x.findings.p2])}
                format={wholeNumber}
                whole
                view={findingsView}
              />
              <p class="panel-foot badge-row">
                {#each CATEGORIES as k (k)}
                  <a class="badge" href={href({ name: 'findings', slug, filter: { category: k } })} title="Findings of this kind first reported in the period">{k} {wholeNumber(c.categories[k])}</a>
                {/each}
              </p>
              <p class="panel-foot"><a href={href({ name: 'findings', slug })}>See every finding</a></p>
            </section>
          </div>

          <section class="panel" aria-labelledby="an-repos">
            <header class="panel-head"><h2 id="an-repos">Most reviewed repositories</h2></header>
            {#if d.repositories.length === 0}
              <p class="state-msg">No reviews in this period.</p>
            {:else}
              <div class="table-wrap">
                <table class="data">
                  <thead>
                    <tr>
                      <th scope="col">Repository</th>
                      <th scope="col" class="num">Reviews</th>
                      <th scope="col">Findings</th>
                      <th scope="col" class="num">Addressed</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each d.repositories as r (r.repository)}
                      {@const n = found(r.findings)}
                      <tr>
                        <td class="mono"><a href={href(repoRoute(slug, r.repository))}>{r.repository}</a></td>
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
        </div>
      {/snippet}
    </StateView>
  </div>
</main>
