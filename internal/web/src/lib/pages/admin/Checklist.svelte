<script lang="ts">
  // What the instance still lacks before it reviews, each step with what to
  // set in the configuration file. Also why the file's latest content was
  // refused, when it was.
  import { Resource } from '../../resource.svelte';
  import { checklist } from '../../setup';
  import type { AdminAccount, SetupStatus } from '../../types';
  import Icon from '../../Icon.svelte';
  import { mdiCheck, mdiCircleOutline } from '../../icons';
  import Collapsible from '../../components/Collapsible.svelte';

  let { accounts }: { accounts: AdminAccount[] } = $props();

  const status = Resource.json<SetupStatus>(() => '/api/v1/admin/setup');
  $effect(() => {
    void status.load();
  });
  const items = $derived(status.data ? checklist(status.data, accounts) : []);
  const done = $derived(items.filter((i) => i.done).length);
  // Once every required step is done the list is reference, not a to-do.
  const complete = $derived(items.every((i) => i.done || i.optional));
</script>

{#if items.length}
  <section class="panel" aria-labelledby="op-setup">
    <header class="panel-head">
      <h2 id="op-setup">Setup</h2>
      <span class="small muted text-ui-sm">{complete ? 'Complete · ' : ''}{done} of {items.length} done</span>
    </header>
    {#if complete}
      <Collapsible title="Steps">{@render steps()}</Collapsible>
    {:else}
      {@render steps()}
    {/if}
  </section>
{/if}

{#snippet steps()}
  <ul class="checklist flex flex-col gap-2 m-0 py-3 px-4 list-none text-ui-md">
    {#each items as item (item.label)}
      <li class:done={item.done}>
        <span class="check-mark inline-flex flex-none mt-0.5 text-muted"><Icon path={item.done ? mdiCheck : mdiCircleOutline} size={14} label={item.done ? 'done' : 'to do'} /></span>
        <span class="check-text flex flex-col gap-0.5">
          <!-- The mustache keeps the separator at the conditional boundary. -->
          <span>{item.label}{#if item.optional}{' '}<span class="small muted text-ui-sm">(optional)</span>{/if}</span>
          {#if !item.done}<span class="small muted text-ui-sm">{item.how}</span>{/if}
        </span>
      </li>
    {/each}
  </ul>
{/snippet}
