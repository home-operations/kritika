<script lang="ts" generics="J extends Job">
  // The jobs of a queue, one row each. account says which account a job is
  // of; with several, each row names its own and links to that account's
  // queue.
  import { href } from '../router.svelte';
  import { jobCauseText, jobTone, shortSha } from '../format';
  import { pullRoute } from '../links';
  import type { Job } from '../types';
  import Pill from './Pill.svelte';
  import Time from './Time.svelte';

  let { jobs, account, several = false }: { jobs: J[]; account: (j: J) => string; several?: boolean } = $props();
</script>

<div class="table-wrap table-card">
  <table class="data">
    <thead>
      <tr>
        {#if several}<th scope="col">Account</th>{/if}
        <th scope="col" class="num">Job</th><th scope="col">Kind</th><th scope="col">State</th><th scope="col" class="num">Attempt</th>
        <th scope="col">Pull</th><th scope="col">Details</th><th scope="col">Scheduled</th><th scope="col">Attempted</th><th scope="col">Last error</th>
      </tr>
    </thead>
    <tbody>
      {#each jobs as j (j.id)}
        <tr>
          {#if several}<td class="mono small"><a href={href({ name: 'queue', slug: account(j) })}>{account(j)}</a></td>{/if}
          <td class="num">{j.id}</td>
          <td>{j.kind}</td>
          <td><Pill tone={jobTone[j.state]} label={j.state} /></td>
          <td class="num">{j.attempt}/{j.maxAttempts}</td>
          <td class="mono small">
            {#if j.args.repository && j.args.number}
              <a href={href(pullRoute(account(j), j.args))}>{j.args.repository}#{j.args.number}</a>
            {:else}{j.args.repository || '—'}{/if}
          </td>
          <td class="small">
            {#if j.args.trigger}<span>{j.args.trigger}</span>{/if}
            {#if j.args.head}<span class="mono" title={j.args.head}>{shortSha(j.args.head)}</span>{/if}
            {#if j.args.commentId}<span>comment {j.args.commentId}</span>{/if}
          </td>
          <td><Time iso={j.scheduledAt} /></td>
          <td><Time iso={j.attemptedAt} /></td>
          <td class="error-cell">{#if j.cause}<strong>{jobCauseText[j.cause]}</strong>{' '}{/if}{j.lastError}</td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
