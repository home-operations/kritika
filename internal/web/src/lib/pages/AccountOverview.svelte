<script lang="ts">
  import { href } from '../router.svelte';
  import { accountApi, repoRoute } from '../links';
  import { Resource, errorMessage, live, pollJobs } from '../resource.svelte';
  import { WANTS, nearCaps } from '../attention';
  import { hookURL } from '../session.svelte';
  import { unsignedWebhooks } from '../setup';
  import { attention as jobNeedsAttention, waiting } from '../queue';
  import { costTotal, tokens, wholeNumber } from '../format';
  import type { AccountDetail, Attention, Job, Page, Repository } from '../types';
  import PageHeader from '../components/PageHeader.svelte';
  import StateView from '../components/StateView.svelte';
  import Pill from '../components/Pill.svelte';
  import WebhookState from '../components/WebhookState.svelte';
  import HelpPopover from '../components/HelpPopover.svelte';
  import StatTile from '../components/StatTile.svelte';

  let { slug }: { slug: string } = $props();
  const base = $derived(accountApi(slug));
  const account = Resource.json<AccountDetail>(() => base);
  const attention = Resource.json<Attention>(() => `${base}/attention`);
  const queue = Resource.json<Job[]>(() => `${base}/queue`);
  const repos = Resource.json<Page<Repository>>(() => `${base}/repos?limit=5`);
  $effect(() => { void account.load(); void attention.load(); void queue.load(); void repos.load(); });
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' || e.kind === 'followup'), () => void account.load()));
  $effect(() => live((e) => e.account === slug && e.kind === 'review', () => void attention.load()));
  $effect(() => live((e) => e.account === slug && e.kind !== 'model_call', () => void queue.load()));
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' || e.kind === 'index_run'), () => void repos.load()));
  $effect(() => pollJobs(() => void queue.load()));

  const wants = $derived(attention.data ? WANTS.filter((w) => attention.data![w.key] > 0) : []);
  const caps = $derived(account.data ? nearCaps(account.data.usage) : []);
  const queueIssue = $derived(queue.data?.some(jobNeedsAttention));
  const connectionIssue = $derived(account.data && (unsignedWebhooks(account.data.connection) || !account.data.connection.lastWebhookAt));
  const complete = $derived(account.data && attention.data && queue.data && !account.error && !attention.error && !queue.error);
</script>

<svelte:head><title>Overview · {slug} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Overview" {slug} description="Review health, active work and repositories for this account.">
      {#snippet actions()}<HelpPopover label="Connection details">{@render connectionDetails()}</HelpPopover><a class="btn" href={href({ name: 'pulls', slug })}>View pull requests →</a><a class="btn" href={href({ name: 'queue', slug })}>Open queue →</a>{/snippet}
    </PageHeader>
    <section class="stats stats-3 account-activity" aria-label="Account activity">
      <StatTile label="Running now" value={queue.data ? wholeNumber(queue.data.filter((j) => j.state === 'running').length) : undefined} pending={!queue.error} href={href({ name: 'queue', slug })} sub={queue.data ? `${queue.data.filter(waiting).length} waiting` : ''} />
      <StatTile label="Reviews today" value={account.data ? wholeNumber(account.data.usage.reviewsToday) : undefined} pending={!account.error} href={href({ name: 'analytics', slug, filter: { days: '1' } })} sub={account.data?.usage.reviewsPerDay ? `Daily limit: ${account.data.usage.reviewsPerDay}` : 'View analytics →'} />
      <StatTile label="API spend this month" value={account.data ? costTotal(account.data.usage.costUsd, account.data.usage.unpricedCalls) : undefined} pending={!account.error} href={href({ name: 'usage', slug })} sub={account.data ? `${tokens(account.data.usage.tokens)} tokens` : ''} />
    </section>
    <section class="panel overview-attention min-w-0" aria-labelledby="account-attention">
      <header class="panel-head"><h2 id="account-attention">Needs attention</h2></header>
      {#each [{ res: account, label: 'Account details' }, { res: attention, label: 'Review alerts' }, { res: queue, label: 'Queue' }] as source (source.label)}
        <span hidden data-loading={source.res.loading || (source.res.data === undefined && !source.res.error)}></span>
        {#if source.res.error}
          <div class="state-msg state-error flex items-center gap-2 m-0 p-3 text-danger border border-solid border-[color-mix(in_srgb,_var(--danger)_35%,_var(--border))] [background:color-mix(in_srgb,_var(--danger)_6%,_transparent)] rounded-md" role="alert"><span>{source.label}: {errorMessage(source.res.error)}</span><button class="btn" onclick={() => void source.res.load()}>Retry {source.label.toLowerCase()}</button></div>
        {:else if source.res.data === undefined}
          <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted" role="status">Loading {source.label.toLowerCase()}…</p>
        {/if}
      {/each}
      {#each wants as want (want.key)}
        <a class="attention-item" href={href({ name: 'pulls', slug, filter: want.filter })}><Pill tone={want.tone} label={`${attention.data![want.key]} ${want.label}`} /><strong>{want.key === 'failed' ? 'Review failures' : want.key === 'capped' ? 'Review limits reached' : want.key === 'p0' ? 'Critical findings' : 'Automatic reviews paused'}</strong><span class="attention-action text-accent text-ui-sm">View pull requests →</span></a>
      {/each}
      {#if queueIssue}
        <a class="attention-item" href={href({ name: 'queue', slug, filter: { state: 'attention' } })}><Pill tone="warn" label="Queue" /><strong>Failed, retrying or overdue work</strong><span class="attention-action text-accent text-ui-sm">Inspect queue →</span></a>
      {/if}
      {#each caps as cap (cap)}
        <a class="attention-item" href={href({ name: 'usage', slug })}><Pill tone="warn" label="Near limit" /><strong>{cap}</strong><span class="attention-action text-accent text-ui-sm">View usage →</span></a>
      {/each}
      {#if connectionIssue}
        <div class="attention-item"><Pill tone="warn" label="Connection" /><strong>{unsignedWebhooks(account.data!.connection) ? 'Unsigned webhooks refused' : 'No webhooks received'}</strong><HelpPopover label="Fix connection">{@render connectionDetails()}</HelpPopover></div>
      {/if}
      {#if complete && !wants.length && !caps.length && !queueIssue && !connectionIssue}
        <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">All clear. No issues need attention.</p>
      {/if}
    </section>
    <section class="panel" aria-labelledby="account-repos">
      <header class="panel-head"><h2 id="account-repos">Repositories</h2><a href={href({ name: 'repos', slug })}>View all repositories →</a></header>
      <StateView res={repos} retry={() => repos.load()} isEmpty={(p) => p.items.length === 0} empty="No repositories connected yet.">
        {#snippet children(p)}
          <div class="table-wrap overflow-x-auto max-w-full"><table class="data"><thead><tr><th scope="col">Repository</th><th scope="col">Reviews</th><th scope="col">Index</th></tr></thead>
            <tbody>{#each p.items as repo (repo.id)}<tr><td class="mono"><a href={href(repoRoute(slug, repo.fullName))}>{repo.fullName}</a></td><td>{repo.enabled ? 'enabled' : 'disabled'}</td><td>{repo.index.lastRunStatus || 'not indexed'}</td></tr>{/each}</tbody>
          </table></div>
        {/snippet}
      </StateView>
    </section>
  </div>
</main>

{#snippet connectionDetails()}
  {#if account.data}
    <p class="mono">{account.data.connection.name}</p>
    <WebhookState of={account.data.connection} polled={{ at: account.data.lastPolledAt }} />
    {#if unsignedWebhooks(account.data.connection)}
      <p role="note">Webhooks with no signature are refused. Set the GitHub App's webhook secret to match kritika.</p>
    {:else if !account.data.connection.lastWebhookAt}
      <p class="wrap-anywhere" role="note">No webhooks received. Point the GitHub App's webhook at <span class="mono">{hookURL(account.data.connection.hookPath)}</span>.</p>
    {/if}
    {#if connectionIssue}<p class="muted">Polling keeps pull requests in sync; mentions need working webhooks.</p>{/if}
  {:else}<p class="muted">{account.error ? 'Connection details unavailable.' : 'Loading connection…'}</p>{/if}
{/snippet}
