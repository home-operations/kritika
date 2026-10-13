<script lang="ts" generics="J extends Job">
  import { href } from '../router.svelte';
  import { jobCauseText, jobTone, shortSha } from '../format';
  import { pullRoute, rerunPath, cancelPath, reindexPath } from '../links';
  import { isAdmin } from '../session.svelte';
  import { waiting, overdue, waitingReason, attention } from '../queue';
  import type { QueueFilter } from '../routes';
  import type { Job } from '../types';
  import Pill from './Pill.svelte';
  import Time from './Time.svelte';
  import Segmented from './Segmented.svelte';
  import ActionDialog from './ActionDialog.svelte';
  import RowToggle from './RowToggle.svelte';
  import { rowToggles } from '../rowclick';
  import type { ComponentProps } from 'svelte';

  let { jobs, account, several = false, ondone, filter, onfilter }: { jobs: J[]; account: (j: J) => string; several?: boolean; ondone?: () => void; filter?: QueueFilter; onfilter: (filter: QueueFilter) => void } = $props();
  const tableId = $props.id();
  let expanded = $state<Record<string, boolean>>({});
  const queueState = $derived(filter?.state ?? 'all');
  const search = $derived(filter?.q ?? '');
  type Action = Omit<ComponentProps<typeof ActionDialog>, 'open' | 'ondone'>;
  let selected = $state<Action>();
  let actionOpen = $state(false);
  function actionFor(j: J): Action | undefined {
    if (j.cancelableReviewId) return { label: 'Cancel review', title: `Cancel review for job #${j.id}?`, body: 'The run stops at its next checkpoint and nothing more is posted to the pull request.', path: cancelPath(account(j), j.cancelableReviewId), danger: true, done: 'Cancel requested' };
    if (j.state !== 'discarded' && j.state !== 'cancelled') return;
    if (j.kind === 'review' && j.args.repository && j.args.number) return { label: 'Re-run', title: `Re-run ${j.args.repository}#${j.args.number}?`, body: "Queue a fresh review of the pull request's current head. This may differ from the failed job's commit. Closed or merged pull requests will be skipped.", path: rerunPath(account(j), j.args), done: 'Re-run queued' };
    if (j.kind === 'index' && j.args.repository) return { label: 'Reindex', title: `Reindex ${j.args.repository}?`, body: "Queue a full index of the repository's current default branch.", path: reindexPath(account(j), j.args.repository), done: 'Reindex queued' };
  }
  const visible = $derived(jobs.filter((j) => {
    const matches = queueState === 'all' || (queueState === 'attention' ? attention(j) : queueState === 'running' ? j.state === 'running' : queueState === 'waiting' ? waiting(j) : !!j.finalizedAt);
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

<div class="toolbar flex flex-wrap items-center gap-2" role="search">
  <label class="search-box [flex:1_1_240px] min-w-0 flex">
    <span class="sr-only">Search jobs</span>
    <input type="search" aria-label="Search jobs" bind:value={() => search, (q) => onfilter({ ...filter, q })} placeholder="Search by repository, account or job" />
  </label>
  <Segmented label="Queue state" options={filters} value={queueState} onchange={(v) => onfilter({ ...filter, state: v === 'all' ? undefined : v })} />
</div>
<div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
  <table class="data queue-table">
    <thead>
      <tr>
        {#if several}<th scope="col">Account</th>{/if}
        <th scope="col">Job / target</th><th scope="col">Status</th><th scope="col">Activity</th><th scope="col">Actions</th><th scope="col" class="relative"><span class="sr-only">Details</span></th>
      </tr>
    </thead>
    <tbody>
      {#each visible as j, i (`${account(j)}:${j.id}`)}
        {@const key = `${account(j)}:${j.id}`}
        {@const previewId = `${tableId}-${i}`}
        <tr class="row-toggles" class:row-expanded={expanded[key]} onclick={(e) => { if (rowToggles(e)) expanded[key] = !expanded[key]; }}>
          {#if several}<td data-label="Account" class="mono small text-ui-sm"><a href={href({ name: 'queue', slug: account(j) })}>{account(j)}</a></td>{/if}
          <td data-label="Job / target"><strong>#{j.id} · {j.kind}</strong>
            {#if j.kind === 'thread' && j.args.resolved !== undefined}<span class="queue-detail block text-muted text-[length:12px] mt-1.5">{j.args.resolved ? 'Resolve thread' : 'Restore thread'}</span>{/if}
            <span class="queue-detail block text-muted text-[length:12px] mt-1.5">{#if j.args.repository && j.args.number}<a class="mono" href={href(pullRoute(account(j), j.args))}>{j.args.repository}#{j.args.number}</a>
            {:else}{j.args.repository || 'Unattributed job'}{/if}</span>
          </td>
          <td data-label="Status"><Pill tone={overdue(j) ? 'warn' : jobTone[j.state]} label={overdue(j) ? 'Heartbeat overdue' : j.state} />
            {#if j.lastError && (j.state === 'discarded' || j.state === 'retryable' || overdue(j))}<span class="queue-detail job-error-summary block text-[length:12px] mt-1.5 max-w-75 overflow-hidden whitespace-nowrap text-ellipsis text-danger">{j.cause ? jobCauseText[j.cause] : j.lastError.split('\n')[0]}</span>{/if}
            {#if j.state === 'retryable'}<span class="queue-detail block text-muted text-[length:12px] mt-1.5">Retries automatically</span>
            {:else if waiting(j) || overdue(j)}<span class="queue-detail block text-muted text-[length:12px] mt-1.5">{waitingReason(j)}</span>{/if}
          </td>
          <td data-label="Activity" class="small text-ui-sm">
            {#if j.finalizedAt}Finished <Time iso={j.finalizedAt} />
            {:else if waiting(j)}Scheduled <Time iso={j.scheduledAt} />
            {:else if j.state === 'running' && j.heartbeatAt}Heartbeat <Time iso={j.heartbeatAt} />
            {:else if j.attemptedAt}Last attempt <Time iso={j.attemptedAt} />
            {:else}Created <Time iso={j.createdAt} />{/if}
          </td>
          <td data-label="Actions">
            {#if isAdmin()}
              {@const action = actionFor(j)}
              <div class="queue-actions flex items-center flex-wrap gap-2.5 mt-0 mx-0 mb-2">
                {#if action}<button class="btn" class:btn-danger={action.danger} onclick={() => { selected = action; actionOpen = true; }}>{action.label}</button>{/if}
              </div>
            {/if}
          </td>
          <td class="row-toggle-cell"><RowToggle bind:open={expanded[key]} label={`Details for job #${j.id}`} controls={previewId} /></td>
        </tr>
        {#if expanded[key]}
          <tr class="row-preview"><td colspan={5 + Number(several)}>
            <div class="row-preview-body" id={previewId} role="region" aria-label={`Job #${j.id} details`}>
              <div class="row-preview-grid">
                <div>
                  <h2 class="preview-heading">Job details</h2>
                  <dl class="preview-facts preview-facts-wide"><dt>Attempt</dt><dd>{j.attempt}/{j.maxAttempts}</dd><dt>Created</dt><dd><Time iso={j.createdAt} /></dd>
                    {#if j.attemptedAt}<dt>Last attempt</dt><dd><Time iso={j.attemptedAt} /></dd>{/if}
                    {#if j.heartbeatAt}<dt>Heartbeat</dt><dd><Time iso={j.heartbeatAt} /></dd>{/if}
                    <dt>Scheduled</dt><dd><Time iso={j.scheduledAt} /></dd>
                    {#if j.args.trigger}<dt>Trigger</dt><dd>{j.args.trigger}</dd>{/if}
                    {#if j.args.head}<dt>Commit</dt><dd><span class="mono" title={j.args.head}>{shortSha(j.args.head)}</span></dd>{/if}
                    {#if j.args.commentId}<dt>Comment</dt><dd>{j.args.commentId}</dd>{/if}
                  </dl>
                </div>
                {#if j.lastError}
                  <div class="min-w-0"><h2 class="preview-heading">Last attempt error</h2><p class="error-text text-danger text-ui-sm whitespace-pre-wrap [word-break:break-word] m-0">{j.lastError}</p></div>
                {/if}
              </div>
            </div>
          </td></tr>
        {/if}
      {/each}
    </tbody>
  </table>
  {#if !visible.length}<p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">{jobs.length ? 'No jobs match these filters.' : 'The queue is empty.'}</p>{/if}
</div>

{#if selected}<ActionDialog {...selected} bind:open={actionOpen} {ondone} />{/if}
