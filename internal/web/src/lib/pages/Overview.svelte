<script lang="ts">
  import PageHeader from '../components/PageHeader.svelte';
  // The instance's landing page: totals across every account the viewer can
  // see, then one row per account with its numbers and its health, each
  // cell that names something wrong leading to where the account shows it.
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import { tokens, costTotal, reviewsLeftOut, usd, wholeNumber } from '../format';
  import type { AccountSummary, InstanceQueue } from '../types';
  import { WANTS, nearCaps } from '../attention';
  import StateView from '../components/StateView.svelte';
  import Meter from '../components/Meter.svelte';
  import Pill from '../components/Pill.svelte';
  import { pullRoute } from '../links';
  import Time from '../components/Time.svelte';
  import { unsignedWebhooks } from '../setup';
  import { overdue, waiting } from '../queue';
  import WebhookState from '../components/WebhookState.svelte';

  const res = new Resource(() => getJSON<AccountSummary[]>('/api/v1/accounts'));

  $effect(() => {
    void res.load();
  });
  $effect(() => live((e) => e.kind !== 'model_call', () => void res.load()));

  // The queue is its own fetch: the page stands without it, and only the
  // one tile waits on it.
  const queue = new Resource(() => getJSON<InstanceQueue>('/api/v1/queue'));
  $effect(() => {
    void queue.load();
  });
  $effect(() => live((e) => e.kind !== 'model_call', () => void queue.load()));
  $effect(() => pollJobs(() => void queue.load()));

  const operationalWants = WANTS.filter((w) => w.key !== 'p0');
  const connectionIssue = (a: AccountSummary) => unsignedWebhooks(a) || !a.lastWebhookAt;
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
      repositories: sum((t) => t.repositories),
      reviews7d: sum((t) => t.reviews7d),
      reviewsToday: sum((t) => t.usage.reviewsToday),
      tokens: sum((t) => t.usage.tokens),
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
        <section class="tiles overview-totals grid grid-cols-[repeat(auto-fit,_minmax(160px,_1fr))] gap-0 border-t border-solid border-t-line pt-3 mt-0" aria-label="Across all accounts">
          <div class="tile bg-panel border border-solid border-line rounded-lg py-2.5 px-3 flex flex-col gap-1">
            <span class="tile-label text-ui-sm text-muted">Review alerts</span>
            <span class="tile-value text-[length:24px] font-bold tabular-nums">{wholeNumber(all.wants.reduce((n, w) => n + w.n, 0))}</span>
            <span class="small muted text-ui-sm">{all.wants.map((w) => `${wholeNumber(w.n)} ${w.label}`).join(' · ') || 'no review failures, caps or pauses'}</span>
          </div>
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
            <span class="tile-label text-ui-sm text-muted">Repositories</span>
            <span class="tile-value text-[length:24px] font-bold tabular-nums">{wholeNumber(all.repositories)}</span>
          </div>
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
          <div class="tile bg-panel border border-solid border-line rounded-lg py-2.5 px-3 flex flex-col gap-1">
            <span class="tile-label text-ui-sm text-muted">Tokens this month</span>
            <span class="tile-value text-[length:24px] font-bold tabular-nums" title={wholeNumber(all.tokens)}>{tokens(all.tokens)}</span>
          </div>
        </section>

        <div class="overview-work grid grid-cols-[minmax(0,_1fr)_minmax(220px,_28%)] gap-7">
          <section class="attention-list overview-attention min-w-0 max-h-95 overflow-y-auto" aria-labelledby="overview-attention">
            <header class="panel-head"><h2 id="overview-attention">Needs attention</h2></header>
            {#each list as account (account.slug)}
              {#each operationalWants.filter((w) => account.attention[w.key] > 0) as w (w.key)}
                <a class="attention-item grid grid-cols-[80px_minmax(0,_1fr)_auto] gap-4 items-center py-5.5 px-0 border-t border-solid border-t-line text-ink" href={href({ name: 'pulls', slug: account.slug, filter: w.filter })}><Pill tone={w.tone} label={`${account.attention[w.key]} ${w.label}`} /><span><strong>{account.slug}</strong><span class="small muted text-ui-sm">View pull requests →</span></span></a>
              {/each}
              {#if connectionIssue(account)}
                <a class="attention-item grid grid-cols-[80px_minmax(0,_1fr)_auto] gap-4 items-center py-5.5 px-0 border-t border-solid border-t-line text-ink" href={href({ name: 'account', slug: account.slug })}>
                  <Pill tone="warn" label="Connection" />
                  <span><strong>{unsignedWebhooks(account) ? 'Unsigned webhooks refused' : 'No webhooks received'}</strong><span class="mono small muted text-ui-sm">{account.slug}</span></span>
                  <span class="attention-action text-accent text-ui-sm">Check connection →</span>
                </a>
              {/if}
              {#each nearCaps(account.usage) as cap (cap)}
                <a class="attention-item grid grid-cols-[80px_minmax(0,_1fr)_auto] gap-4 items-center py-5.5 px-0 border-t border-solid border-t-line text-ink" href={href({ name: 'usage', slug: account.slug })}>
                  <Pill tone="warn" label="Near limit" />
                  <span><strong>{cap}</strong><span class="mono small muted text-ui-sm">{account.slug}</span></span>
                  <span class="attention-action text-accent text-ui-sm">View usage →</span>
                </a>
              {/each}
            {/each}
            {#if !all.wants.length && !list.some((a) => nearCaps(a.usage).length || connectionIssue(a))}
              <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">All clear. No failed or capped reviews, paused pull requests, connection issues or approaching usage limits.</p>
            {/if}
          </section>
          <section class="running-list min-w-0 pl-6 border-l border-solid border-l-line" aria-labelledby="overview-running">
            <header class="panel-head"><h2 id="overview-running">Running work</h2></header>
            <StateView res={queue} retry={() => queue.load()}>
              {#snippet children(q)}
                {@const pressure = work(q)}
                <p class="panel-body small text-ui-sm py-2.5 px-3">{pressure.running} running · {pressure.waiting} waiting{pressure.slots ? ` · ${pressure.held} of ${pressure.slots} model slots busy` : ''}</p>
                {#each q.slots.filter((s) => s.slots > 0 && s.held >= s.slots) as slot (`${slot.account}/${slot.model}`)}
                  <a class="attention-item grid grid-cols-[80px_minmax(0,_1fr)_auto] gap-4 items-center py-5.5 px-0 border-t border-solid border-t-line text-ink" href={href({ name: 'queue', slug: slot.account })}><Pill tone="warn" label="At capacity" /><span>{slot.account}<span class="queue-detail block text-muted text-[length:12px] mt-1.5">{slot.model}: {slot.held} of {slot.slots} slots busy</span></span></a>
                {/each}
                {#each q.jobs.filter((j) => j.recentFailure).slice(0, 5) as job (job.id)}
                  <a class="attention-item grid grid-cols-[80px_minmax(0,_1fr)_auto] gap-4 items-center py-5.5 px-0 border-t border-solid border-t-line text-ink" href={href({ name: 'queue', slug: job.account })}><Pill tone="danger" label="Failed job" /><span>{job.args.repository || job.account}<span class="queue-detail block text-muted text-[length:12px] mt-1.5">{job.kind} #{job.id} · inspect error and recovery options</span></span></a>
                {/each}
                {#each q.jobs.filter((j) => j.state === 'running').slice(0, 5) as job (job.id)}
                  <div class="running-item flex flex-col items-start gap-2.5 py-5 px-0 border-t border-solid border-t-line wrap-anywhere">
                    <Pill tone={overdue(job) ? 'warn' : 'accent'} label={overdue(job) ? 'Heartbeat overdue' : job.kind} />
                    {#if job.args.repository && job.args.number}
                      <a class="mono" href={href(pullRoute(job.account, job.args))}>{job.args.repository}#{job.args.number}</a>
                    {:else}<a href={href({ name: 'queue', slug: job.account })}>{job.kind} job {job.id}</a>{/if}
                    <span class="small muted text-ui-sm">{job.account} · started <Time iso={job.attemptedAt} /></span>
                  </div>
                {:else}<p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">Nothing running right now.</p>{/each}
                <a class="running-queue inline-block pt-4" href={href({ name: 'instanceQueue' })}>Open queue →</a>
              {/snippet}
            </StateView>
          </section>
        </div>



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
