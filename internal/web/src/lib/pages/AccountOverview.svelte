<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { accountApi, repoRoute } from '../links';
  import { Resource, errorMessage, live, pollJobs } from '../resource.svelte';
  import { WANTS, nearCaps } from '../attention';
  import { unsignedWebhooks } from '../setup';
  import { attention as jobNeedsAttention, waiting } from '../queue';
  import { costTotal, tokens } from '../format';
  import type { Route } from '../routes';
  import type { AccountDetail, Attention, Job, Page, Repository } from '../types';
  import PageHeader from '../components/PageHeader.svelte';
  import StateView from '../components/StateView.svelte';
  import Pill from '../components/Pill.svelte';
  import WebhookState from '../components/WebhookState.svelte';

  let { slug }: { slug: string } = $props();
  const base = $derived(accountApi(slug));
  const account = new Resource(() => getJSON<AccountDetail>(base));
  const attention = new Resource(() => getJSON<Attention>(`${base}/attention`));
  const queue = new Resource(() => getJSON<Job[]>(`${base}/queue`));
  const repos = new Resource(() => getJSON<Page<Repository>>(`${base}/repos?limit=5`));
  $effect(() => { void account.load(); void attention.load(); void queue.load(); void repos.load(); });
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' || e.kind === 'followup'), () => void account.load()));
  $effect(() => live((e) => e.account === slug && e.kind === 'review', () => void attention.load()));
  $effect(() => live((e) => e.account === slug && e.kind !== 'model_call', () => void queue.load()));
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' || e.kind === 'index_run'), () => void repos.load()));
  $effect(() => pollJobs(() => void queue.load()));

  const wants = $derived(attention.data ? WANTS.filter((w) => attention.data![w.key] > 0) : []);
  const caps = $derived(account.data ? nearCaps(account.data.usage) : []);
  const queueIssue = $derived(queue.data?.some(jobNeedsAttention));
  const complete = $derived(account.data && attention.data && queue.data && !account.error && !attention.error && !queue.error);
</script>

<svelte:head><title>Overview · {slug} · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <PageHeader title="Overview" {slug} description="Review health, active work and repositories for this account.">
      {#snippet actions()}<a class="btn" href={href({ name: 'pulls', slug })}>View pull requests →</a>{/snippet}
    </PageHeader>
    <section class="tiles account-activity" aria-label="Account activity">
      {@render metric('Running now', queue.data?.filter((j) => j.state === 'running').length, { name: 'queue', slug, filter: { state: 'running' } }, 'Open running work →', !queue.error)}
      {@render metric('Waiting', queue.data?.filter(waiting).length, { name: 'queue', slug, filter: { state: 'waiting' } }, 'Open waiting work →', !queue.error)}
      {@render metric('API spend this month', account.data ? costTotal(account.data.usage.costUsd, account.data.usage.unpricedCalls) : undefined, { name: 'usage', slug }, account.data ? `${tokens(account.data.usage.tokens)} tokens` : '', !account.error)}
      {@render metric('Reviews today', account.data?.usage.reviewsToday, { name: 'analytics', slug, filter: { days: '1' } }, 'View analytics →', !account.error)}
    </section>
    <div class="overview-work">
      <section class="attention-list" aria-labelledby="account-attention">
        <header class="panel-head"><h2 id="account-attention">Needs attention</h2></header>
        {#each [{ res: account, label: 'Account details' }, { res: attention, label: 'Review alerts' }, { res: queue, label: 'Queue' }] as source (source.label)}
          <span hidden data-loading={source.res.loading || (source.res.data === undefined && !source.res.error)}></span>
          {#if source.res.error}
            <div class="state-msg state-error" role="alert"><span>{source.label}: {errorMessage(source.res.error)}</span><button class="btn" onclick={() => void source.res.load()}>Retry {source.label.toLowerCase()}</button></div>
          {:else if source.res.data === undefined}
            <p class="state-msg" role="status">Loading {source.label.toLowerCase()}…</p>
          {/if}
        {/each}
        {#each wants as want (want.key)}
          <a class="attention-item" href={href({ name: 'pulls', slug, filter: want.filter })}><Pill tone={want.tone} label={`${attention.data![want.key]} ${want.label}`} /><span>View pull requests →</span></a>
        {/each}
        {#if queueIssue}
          <a class="attention-item" href={href({ name: 'queue', slug, filter: { state: 'attention' } })}><Pill tone="warn" label="Queue" /><span>Inspect failed, retrying or overdue work →</span></a>
        {/if}
        {#each caps as cap (cap)}
          <a class="attention-item" href={href({ name: 'usage', slug })}><Pill tone="warn" label="Near limit" /><span>{cap}</span></a>
        {/each}
        {#if complete && !wants.length && !caps.length && !queueIssue}
          <p class="state-msg">All clear. No review alerts or queue issues.</p>
        {/if}
      </section>
      <section class="panel" aria-labelledby="account-connection">
        <header class="panel-head"><h2 id="account-connection">Connection</h2></header>
        <div class="panel-body">
          {#if account.data}
            <p class="mono">{account.data.connection.name}</p>
            <WebhookState of={account.data.connection} polled={{ at: account.data.lastPolledAt }} />
            <p class="small muted">{unsignedWebhooks(account.data.connection) ? 'Unsigned webhooks are refused. Configure a webhook secret.' : account.data.connection.lastWebhookAt ? 'Receiving webhook events.' : 'Waiting for webhook events; polling keeps pull requests in sync.'}</p>
          {:else}<p class="small muted">{account.error ? 'Connection details unavailable.' : 'Loading connection…'}</p>{/if}
        </div>
      </section>
    </div>
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

{#snippet metric(label: string, value: string | number | undefined, route: Route, detail: string, pending: boolean)}
  <a class="tile" href={href(route)} aria-busy={value === undefined && pending}>
    <span class="tile-label">{label}</span>
    {#if value !== undefined}<span class="tile-value">{value}</span><span class="small muted">{detail}</span>
    {:else if pending}<span class="skeleton-value" aria-label={`Loading ${label.toLowerCase()}`}></span><span class="skeleton-line"></span>
    {:else}<span class="small muted">Unavailable</span>{/if}
  </a>
{/snippet}
