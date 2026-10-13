<script lang="ts">
  import PageHeader from '../components/PageHeader.svelte';
  // The instance's landing page: totals across every account the viewer can
  // see, then one row per account with its numbers and its health, each
  // cell that names something wrong leading to where the account shows it.
  import { href } from '../router.svelte';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import { tokens, costTotal, reviewsLeftOut, usd, wholeNumber } from '../format';
  import type { AccountSummary, InstanceQueue } from '../types';
  import { WANTS, nearCaps } from '../attention';
  import StateView from '../components/StateView.svelte';
  import Meter from '../components/Meter.svelte';
  import Pill from '../components/Pill.svelte';
  import { unsignedWebhooks } from '../setup';
  import { overdue, waiting } from '../queue';
  import WebhookState from '../components/WebhookState.svelte';

  const res = Resource.json<AccountSummary[]>(() => '/api/v1/accounts');

  $effect(() => {
    void res.load();
  });
  $effect(() => live((e) => e.kind !== 'model_call', () => void res.load()));

  // The queue is its own fetch: the page stands without it, and only the
  // one tile waits on it.
  const queue = Resource.json<InstanceQueue>(() => '/api/v1/queue');
  $effect(() => {
    void queue.load();
  });
  $effect(() => live((e) => e.kind !== 'model_call', () => void queue.load()));
  $effect(() => pollJobs(() => void queue.load()));

  const operationalWants = WANTS.filter((w) => w.key !== 'p0');
  const connectionIssue = (a: AccountSummary) => unsignedWebhooks(a) || !a.lastWebhookAt;
  const fullSlots = $derived(queue.data?.slots.filter((s) => s.slots > 0 && s.held >= s.slots) ?? []);
  const queueIssues = $derived(queue.data?.jobs.filter((j) => j.recentFailure || overdue(j)) ?? []);
  function work(q: InstanceQueue) {
    const capped = q.slots.filter((s) => s.slots > 0);
    return {
      running: q.jobs.filter((j) => j.state === 'running').length,
      waiting: q.jobs.filter(waiting).length,
      held: capped.reduce((n, s) => n + s.held, 0),
      slots: capped.reduce((n, s) => n + s.slots, 0),
    };
  }

  function totals(list: AccountSummary[]) {
    const sum = (f: (t: AccountSummary) => number) => list.reduce((n, t) => n + f(t), 0);
    return {
      reviews7d: sum((t) => t.reviews7d),
      reviewsToday: sum((t) => t.usage.reviewsToday),
      costUsd: sum((t) => t.usage.costUsd),
      unpricedCalls: sum((t) => t.usage.unpricedCalls ?? 0),
      unpricedReviews: sum((t) => t.usage.unpricedReviews ?? 0),
      reviews: sum((t) => t.usage.reviews),
      pricedReviews: sum((t) => t.usage.reviews - (t.usage.unpricedReviews ?? 0)),
      reviewCostUsd: sum((t) => t.usage.reviewCostUsd),
      wants: operationalWants.map((w) => ({ label: w.label, n: sum((t) => t.attention[w.key]) })).filter((w) => w.n > 0),
    };
  }
</script>

<svelte:head><title>All accounts · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Operations overview" description="Service health, running work and usage across your accounts.">
      {#snippet actions()}<a class="btn" href={href({ name: 'instancePulls' })}>View pull requests →</a><a class="btn" href={href({ name: 'instanceQueue' })}>Open queue →</a>{/snippet}
    </PageHeader>
    <StateView {res} layout="overview" retry={() => res.load()} isEmpty={(d) => d.length === 0} empty="You are not a member of any account yet.">
      {#snippet children(list)}
        {@const all = totals(list)}
        <section class="tiles operations-summary panel grid grid-cols-3 max-[800px]:grid-cols-1" aria-label="Across all accounts">
          {#if queue.data}
            {@const w = work(queue.data)}
            <a class="tile bg-panel border border-solid border-line rounded-lg py-2.5 px-3 flex flex-col gap-1" href={href({ name: 'instanceQueue' })}>
              <span class="tile-label text-ui-sm text-muted">Running now</span>
              <span class="tile-value text-[length:24px] font-bold tabular-nums">{wholeNumber(w.running)}</span>
              <span class="small muted text-ui-sm">{wholeNumber(w.waiting)} waiting{w.slots ? ` · ${wholeNumber(w.held)} of ${wholeNumber(w.slots)} model slots busy` : ''}</span>
            </a>
          {:else}
            <div class="tile bg-panel border border-solid border-line rounded-lg py-2.5 px-3 flex flex-col gap-1" aria-busy={!queue.error}>
              <span class="tile-label text-ui-sm text-muted">Running now</span>
              {#if queue.error}<span class="small muted text-ui-sm">Unavailable</span><button class="btn btn-small" onclick={() => void queue.load()}>Retry queue</button>
              {:else}<span class="skeleton-value block bg-line rounded-[4px] w-14 h-9" aria-label="Loading running work"></span><span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span>{/if}
            </div>
          {/if}
          <div class="tile bg-panel border border-solid border-line rounded-lg py-2.5 px-3 flex flex-col gap-1">
            <span class="tile-label text-ui-sm text-muted">Reviews, last 7 days</span>
            <span class="tile-value text-[length:24px] font-bold tabular-nums">{wholeNumber(all.reviews7d)}</span>
            <span class="small muted text-ui-sm">{wholeNumber(all.reviewsToday)} today</span>
          </div>
          <div class="tile bg-panel border border-solid border-line rounded-lg py-2.5 px-3 flex flex-col gap-1">
            <span class="tile-label text-ui-sm text-muted">API spend this month</span>
            <span class="tile-value text-[length:24px] font-bold tabular-nums">{costTotal(all.costUsd, all.unpricedCalls)}</span>
            <!-- The mean, not a median: each account reports its own reviews and a median does not add up. -->
            <span class="small muted text-ui-sm"
              >{!all.reviews
                ? 'no review completed this month'
                : !all.pricedReviews
                  ? `${wholeNumber(all.reviews)} reviews this month, all with unpriced calls`
                  : `${usd(all.reviewCostUsd / all.pricedReviews)} per review, mean of ${wholeNumber(all.pricedReviews)}${reviewsLeftOut(all.unpricedReviews)}`}</span
            >
          </div>
        </section>

        <section class="panel overview-attention min-w-0" aria-labelledby="overview-attention">
          <header class="panel-head"><h2 id="overview-attention">Needs attention</h2></header>
          {#each list as account (account.slug)}
            {#each operationalWants.filter((w) => account.attention[w.key] > 0) as w (w.key)}
              <a class="attention-item" href={href({ name: 'pulls', slug: account.slug, filter: w.filter })}><Pill tone={w.tone} label={`${account.attention[w.key]} ${w.label}`} /><strong>{account.slug}</strong><span class="attention-action text-accent text-ui-sm">View pull requests →</span></a>
            {/each}
            {#if connectionIssue(account)}
              <a class="attention-item" href={href({ name: 'account', slug: account.slug })}>
                <Pill tone="warn" label="Connection" />
                <span><strong>{unsignedWebhooks(account) ? 'Unsigned webhooks refused' : 'No webhooks received'}</strong><span class="mono small muted text-ui-sm">{account.slug}</span></span>
                <span class="attention-action text-accent text-ui-sm">Check connection →</span>
              </a>
            {/if}
            {#each nearCaps(account.usage) as cap (cap)}
              <a class="attention-item" href={href({ name: 'usage', slug: account.slug })}>
                <Pill tone="warn" label="Near limit" />
                <span><strong>{cap}</strong><span class="mono small muted text-ui-sm">{account.slug}</span></span>
                <span class="attention-action text-accent text-ui-sm">View usage →</span>
              </a>
            {/each}
          {/each}
          {#each fullSlots as slot (`${slot.account}/${slot.model}`)}
            <a class="attention-item" href={href({ name: 'queue', slug: slot.account })}><Pill tone="warn" label="At capacity" /><span><strong>{slot.account}</strong><span class="small muted text-ui-sm wrap-anywhere">{slot.model}: {slot.held} of {slot.slots} slots busy</span></span><span class="attention-action text-accent text-ui-sm">Open queue →</span></a>
          {/each}
          {#each queueIssues.slice(0, 5) as job (job.id)}
            <a class="attention-item" href={href({ name: 'queue', slug: job.account, filter: { state: 'attention' } })}><Pill tone={overdue(job) ? 'warn' : 'danger'} label={overdue(job) ? 'Heartbeat overdue' : 'Failed job'} /><span><strong>{job.args.repository || job.account}</strong><span class="small muted text-ui-sm">{job.kind} #{job.id}</span></span><span class="attention-action text-accent text-ui-sm">Inspect job →</span></a>
          {/each}
          {#if queue.error}<p class="state-msg flex items-center gap-2 m-0 p-3 text-danger" role="alert">Queue alerts unavailable.{#if queue.data}<button class="btn btn-small" onclick={() => void queue.load()}>Retry queue alerts</button>{/if}</p>
          {:else if !queue.data}<p class="state-msg m-0 p-3 text-muted" role="status">Checking queue…</p>
          {:else if !all.wants.length && !list.some((a) => nearCaps(a.usage).length || connectionIssue(a)) && !fullSlots.length && !queueIssues.length}
            <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">All clear. No issues need attention.</p>
          {/if}
        </section>
        <section class="panel" aria-labelledby="account-breakdown">
          <header class="panel-head"><h2 id="account-breakdown">By account</h2></header>
          <div class="table-wrap overflow-x-auto max-w-full">
            <table class="data account-breakdown">
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Connection</th>
                  <th scope="col" class="num">Repos</th>
                  <th scope="col" class="num">Reviews 7d</th>
                  <th scope="col" title="Reviews done today, against the account's daily cap where it has one">Today</th>
                  <th scope="col" title="Open pull requests whose last review failed, hit a limit or found a P0, or whose automatic reviews are paused; and a cap that is close">Needs attention</th>
                  <th scope="col" title="Whether GitHub's webhooks reach kritika, and when it last polled instead">Webhooks</th>
                  <th scope="col" class="num wrap">API spend</th>
                  <th scope="col">Tokens this month</th>
                </tr>
              </thead>
              <tbody>
                {#each list as t (t.slug)}
                  <tr>
                    <td class="mono name-fill" title={t.slug}><a href={href({ name: 'account', slug: t.slug })}>{t.slug}</a></td>
                    <td class="mono small name-clip text-ui-sm" title={t.connection}>{t.connection}</td>
                    <td class="num">{wholeNumber(t.repositories)}</td>
                    <td class="num">{wholeNumber(t.reviews7d)}</td>
                    <td>
                      <span class="small text-ui-sm">
                        {wholeNumber(t.usage.reviewsToday)}{t.usage.reviewsPerDay ? ` of ${wholeNumber(t.usage.reviewsPerDay)}` : ''}
                      </span>
                      {#if t.usage.reviewsPerDay}
                        <Meter value={t.usage.reviewsToday} max={t.usage.reviewsPerDay} label={`Today's reviews done by ${t.slug}`} />
                      {/if}
                    </td>
                    <td>
                      <span class="account-wants inline-flex flex-wrap gap-1 min-w-45">
                        {#each WANTS.filter((w) => t.attention[w.key] > 0) as w (w.key)}
                          <a href={href({ name: 'pulls', slug: t.slug, filter: w.filter })} title="{t.attention[w.key]} open pull requests {w.why}">
                            <Pill tone={w.tone} label={`${t.attention[w.key]} ${w.label}`} />
                          </a>
                        {/each}
                        {#each nearCaps(t.usage) as text (text)}
                          <a href={href({ name: 'usage', slug: t.slug })} title={text}><Pill tone="warn" label="cap" /></a>
                        {/each}
                        {#if !WANTS.some((w) => t.attention[w.key] > 0) && !nearCaps(t.usage).length}<span class="muted">—</span>{/if}
                      </span>
                    </td>
                    <td>
                      <WebhookState of={t} polled={{ at: t.lastPolledAt }} />
                    </td>
                    <td class="num"><a href={href({ name: 'usage', slug: t.slug })}>{costTotal(t.usage.costUsd, t.usage.unpricedCalls)}</a></td>
                    <td>
                      <span class="small text-ui-sm">
                        {tokens(t.usage.tokens)}{t.usage.tokensPerMonth ? ` of ${tokens(t.usage.tokensPerMonth)}` : ''}
                      </span>
                      {#if t.usage.tokensPerMonth}
                        <Meter value={t.usage.tokens} max={t.usage.tokensPerMonth} label={`Monthly tokens used by ${t.slug}`} />
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/snippet}
    </StateView>
  </div>
</main>
