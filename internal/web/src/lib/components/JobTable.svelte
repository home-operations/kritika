<script lang="ts" generics="J extends Job">
  import { href } from '../router.svelte';
  import { jobCauseText, jobTone, shortSha } from '../format';
  import { pullRoute, rerunPath, cancelPath, reindexPath } from '../links';
  import { isAdmin } from '../session.svelte';
  import { clock } from '../time.svelte';
  import { waiting, overdue, waitingReason } from '../queue';
  import type { Job } from '../types';
  import Pill from './Pill.svelte';
  import Time from './Time.svelte';
  import Segmented from './Segmented.svelte';
  import ActionButton from './ActionButton.svelte';

  let { jobs, account, several = false, ondone }: { jobs: J[]; account: (j: J) => string; several?: boolean; ondone?: () => void } = $props();
  let filter = $state<'all' | 'attention' | 'running' | 'waiting' | 'finished'>('all');
  let search = $state('');
  const attention = (j: Job) => j.state === 'discarded' || j.state === 'retryable' || overdue(j, clock.now);
  const visible = $derived(jobs.filter((j) => {
    const matches = filter === 'all' || (filter === 'attention' ? attention(j) : filter === 'running' ? j.state === 'running' : filter === 'waiting' ? waiting(j) : !!j.finalizedAt);
    return matches && `${account(j)} ${j.args.repository} ${j.args.number} ${j.kind} ${j.id}`.toLowerCase().includes(search.toLowerCase());
  }));
  const filters = $derived([
    { value: 'all', label: `All (${jobs.length})` },
    { value: 'attention', label: `Needs attention (${jobs.filter(attention).length})` },
    { value: 'running', label: `Running (${jobs.filter((j) => j.state === 'running').length})` },
    { value: 'waiting', label: `Waiting (${jobs.filter(waiting).length})` },
    { value: 'finished', label: 'Finished' },
  ] as const);
</script>

<div class="toolbar queue-toolbar">
  <Segmented label="Queue state" options={filters} value={filter} onchange={(v) => (filter = v)} />
  <label class="queue-search">Search jobs <input type="search" bind:value={search} placeholder="Repository, account or job" /></label>
</div>
<p class="small muted">Showing {visible.length} of {jobs.length} loaded jobs. Up to 500 active and 50 finished jobs per account. Refreshes every 15 seconds.</p>
<div class="table-wrap table-card">
  <table class="data queue-table">
    <thead>
      <tr>
        {#if several}<th scope="col">Account</th>{/if}
        <th scope="col">Job</th><th scope="col">State / waiting reason</th><th scope="col">Pull</th>
        <th scope="col">Activity</th><th scope="col">Last error / actions</th>
      </tr>
    </thead>
    <tbody>
      {#each visible as j (j.id)}
        <tr>
          {#if several}<td data-label="Account" class="mono small"><a href={href({ name: 'queue', slug: account(j) })}>{account(j)}</a></td>{/if}
          <td data-label="Job"><strong>#{j.id} · {j.kind}</strong><span class="queue-detail">Attempt {j.attempt}/{j.maxAttempts}</span></td>
          <td data-label="State"><Pill tone={overdue(j, clock.now) ? 'warn' : jobTone[j.state]} label={overdue(j, clock.now) ? 'Heartbeat overdue' : j.state} /><span class="queue-detail">{waitingReason(j, clock.now)}</span></td>
          <td data-label="Pull" class="small">
            {#if j.args.repository && j.args.number}
              <a class="mono" href={href(pullRoute(account(j), j.args))}>{j.args.repository}#{j.args.number}</a>
            {:else}{j.args.repository || '—'}{/if}
            <span class="queue-detail">{j.args.trigger} {shortSha(j.args.head)}</span>
            {#if j.args.commentId}<span class="queue-detail">comment {j.args.commentId}</span>{/if}
          </td>
          <td data-label="Activity" class="small">
            {#if j.finalizedAt}Finished <Time iso={j.finalizedAt} />
            {:else if j.state === 'running' && j.heartbeatAt}Heartbeat <Time iso={j.heartbeatAt} />
            {:else if j.attemptedAt}Last attempt <Time iso={j.attemptedAt} />
            {:else}Created <Time iso={j.createdAt} />{/if}
            {#if waiting(j)}<span class="queue-detail">Scheduled <Time iso={j.scheduledAt} /></span>{/if}
          </td>
          <td data-label="Error / actions" class="queue-error">
            {#if j.lastError}<details><summary>{j.cause ? jobCauseText[j.cause] : 'Last attempt error'}</summary><p class="error-text">{j.lastError}</p></details>{/if}
            {#if isAdmin()}
              <div class="queue-actions">
                {#if j.cancelableReviewId}
                  <ActionButton label="Cancel review" title={`Cancel review for job #${j.id}?`} body="The run stops at its next checkpoint and nothing more is posted to the pull request." path={cancelPath(account(j), j.cancelableReviewId)} danger done="Cancel requested" {ondone} />
                {:else if (j.state === 'discarded' || j.state === 'cancelled') && j.kind === 'review' && j.args.repository && j.args.number}
                  <ActionButton label="Re-run" title={`Re-run ${j.args.repository}#${j.args.number}?`} body="Queue a fresh review of the pull request's current head. This may differ from the failed job's commit. Closed or merged pull requests will be skipped." path={rerunPath(account(j), j.args)} done="Re-run queued" {ondone} />
                {:else if (j.state === 'discarded' || j.state === 'cancelled') && j.kind === 'index' && j.args.repository}
                  <ActionButton label="Reindex" title={`Reindex ${j.args.repository}?`} body="Queue a full index of the repository's current default branch." path={reindexPath(account(j), j.args.repository)} done="Reindex queued" {ondone} />
                {/if}
              </div>
            {/if}
            {#if j.state === 'retryable'}<span class="queue-detail">Retries automatically; no manual action needed.</span>{/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
  {#if !visible.length}<p class="state-msg">No jobs match these filters.</p>{/if}
</div>
