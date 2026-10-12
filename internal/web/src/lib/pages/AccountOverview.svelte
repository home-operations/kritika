<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { accountApi, repoRoute } from '../links';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import { WANTS, nearCaps } from '../attention';
  import { unsignedWebhooks } from '../setup';
  import { waiting } from '../queue';
  import { costTotal, tokens } from '../format';
  import type { AccountDetail, Attention, Job, Page, Repository } from '../types';
  import PageHeader from '../components/PageHeader.svelte';
  import StateView from '../components/StateView.svelte';
  import Pill from '../components/Pill.svelte';
  import WebhookState from '../components/WebhookState.svelte';

  let { slug }: { slug: string } = $props();
  const base = $derived(accountApi(slug));
  const res = new Resource(async () => {
    const [account, attention, jobs] = await Promise.all([
      getJSON<AccountDetail>(base),
      getJSON<Attention>(`${base}/attention`),
      getJSON<Job[]>(`${base}/queue`),
    ]);
    return { account, attention, jobs };
  });
  const repos = new Resource(() => getJSON<Page<Repository>>(`${base}/repos?limit=5`));
  $effect(() => { void res.load(); void repos.load(); });
  $effect(() => live((e) => e.account === slug && e.kind !== 'model_call', () => { void res.load(); void repos.load(); }));
  $effect(() => pollJobs(() => void res.load()));
</script>

<svelte:head><title>Overview · {slug} · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <PageHeader title="Overview" {slug} description="Review health, active work and repositories for this account.">
      {#snippet actions()}<a class="btn" href={href({ name: 'pulls', slug })}>View pull requests →</a>{/snippet}
    </PageHeader>
    <StateView {res} retry={() => res.load()} layout="account">
      {#snippet children({ account, attention, jobs })}
        <section class="tiles account-activity" aria-label="Account activity">
          <a class="tile" href={href({ name: 'queue', slug, filter: { state: 'running' } })}>
            <span class="tile-label">Running now</span><span class="tile-value">{jobs.filter((j) => j.state === 'running').length}</span><span class="small muted">Open running work →</span>
          </a>
          <a class="tile" href={href({ name: 'queue', slug, filter: { state: 'waiting' } })}>
            <span class="tile-label">Waiting</span><span class="tile-value">{jobs.filter(waiting).length}</span><span class="small muted">Open waiting work →</span>
          </a>
          <a class="tile" href={href({ name: 'usage', slug })}>
            <span class="tile-label">API spend this month</span><span class="tile-value">{costTotal(account.usage.costUsd, account.usage.unpricedCalls)}</span><span class="small muted">{tokens(account.usage.tokens)} tokens</span>
          </a>
          <a class="tile" href={href({ name: 'analytics', slug, filter: { days: '1' } })}>
            <span class="tile-label">Reviews today</span><span class="tile-value">{account.usage.reviewsToday}</span><span class="small muted">View analytics →</span>
          </a>
        </section>
        <div class="overview-work">
          <section class="attention-list" aria-labelledby="account-attention">
            <header class="panel-head"><h2 id="account-attention">Needs attention</h2></header>
            {#each WANTS.filter((w) => attention[w.key] > 0) as want (want.key)}
              <a class="attention-item" href={href({ name: 'pulls', slug, filter: want.filter })}><Pill tone={want.tone} label={`${attention[want.key]} ${want.label}`} /><span>View pull requests →</span></a>
            {/each}
            {#if jobs.some((j) => j.recentFailure || j.heartbeatOverdue)}
              <a class="attention-item" href={href({ name: 'queue', slug, filter: { state: 'attention' } })}><Pill tone="warn" label="Queue" /><span>Inspect failed or overdue work →</span></a>
            {/if}
            {#each nearCaps(account.usage) as cap (cap)}
              <a class="attention-item" href={href({ name: 'usage', slug })}><Pill tone="warn" label="Near limit" /><span>{cap}</span></a>
            {/each}
            {#if !WANTS.some((w) => attention[w.key] > 0) && !nearCaps(account.usage).length && !jobs.some((j) => j.recentFailure || j.heartbeatOverdue)}
              <p class="state-msg">All clear. No review alerts or queue issues.</p>
            {/if}
          </section>
          <section class="panel" aria-labelledby="account-connection">
            <header class="panel-head"><h2 id="account-connection">Connection</h2></header>
            <div class="panel-body">
              <p class="mono">{account.connection.name}</p>
              <WebhookState of={account.connection} polled={{ at: account.lastPolledAt }} />
              <p class="small muted">{unsignedWebhooks(account.connection) ? 'Unsigned webhooks are refused. Configure a webhook secret.' : account.connection.lastWebhookAt ? 'Receiving webhook events.' : 'Waiting for webhook events; polling keeps pull requests in sync.'}</p>
            </div>
          </section>
        </div>
      {/snippet}
    </StateView>
    <section class="panel" aria-labelledby="account-repos">
      <header class="panel-head"><h2 id="account-repos">Repositories</h2><a href={href({ name: 'repos', slug })}>View all repositories →</a></header>
      <StateView res={repos} retry={() => repos.load()} isEmpty={(p) => p.items.length === 0} empty="No repositories connected yet.">
        {#snippet children(p)}
          <div class="table-wrap"><table class="data"><thead><tr><th scope="col">Repository</th><th scope="col">Reviews</th><th scope="col">Index</th></tr></thead>
            <tbody>{#each p.items as repo (repo.id)}<tr><td class="mono"><a href={href(repoRoute(slug, repo.fullName))}>{repo.fullName}</a></td><td>{repo.enabled ? 'Enabled' : 'Disabled'}</td><td>{repo.index.lastRunStatus || 'Not indexed'}</td></tr>{/each}</tbody>
          </table></div>
        {/snippet}
      </StateView>
    </section>
  </div>
</main>
