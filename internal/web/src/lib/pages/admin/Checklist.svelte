<script lang="ts">
  // What the instance still lacks before it reviews, each step with what to
  // set in the configuration file, and why the file's latest content was
  // refused, when it was.
  import { Resource } from '../../resource.svelte';
  import { checklist } from '../../setup';
  import type { AdminAccount, SetupStatus } from '../../types';
  import Icon from '../../Icon.svelte';
  import { mdiCheck, mdiCircleOutline } from '../../icons';

  let { accounts }: { accounts: AdminAccount[] } = $props();

  const status = Resource.json<SetupStatus>(() => '/api/v1/admin/setup');
  $effect(() => {
    void status.load();
  });
  const items = $derived(status.data ? checklist(status.data, accounts) : []);
</script>

{#if items.length}
  <section class="panel" aria-labelledby="op-setup">
    <header class="panel-head">
      <h2 id="op-setup">Setup</h2>
      <span class="small muted text-ui-sm">{items.filter((i) => i.done).length} of {items.length} done</span>
    </header>
    <ul class="checklist flex flex-col gap-2 m-0 py-2.5 px-3 list-none text-ui-md">
      {#each items as item (item.label)}
        <li class:done={item.done}>
          <span class="check-mark inline-flex flex-none mt-0.5 text-muted"><Icon path={item.done ? mdiCheck : mdiCircleOutline} size={14} label={item.done ? 'done' : 'to do'} /></span>
          <span class="check-text flex flex-col gap-0.5">
            <!-- eslint-disable-next-line svelte/no-useless-mustaches -- Preserve the separator at the conditional boundary. -->
            <span>{item.label}{#if item.optional}{' '}<span class="small muted text-ui-sm">(optional)</span>{/if}</span>
            {#if !item.done}<span class="small muted text-ui-sm">{item.how}</span>{/if}
          </span>
        </li>
      {/each}
    </ul>
  </section>
{/if}
