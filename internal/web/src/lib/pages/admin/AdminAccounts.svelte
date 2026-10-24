<script lang="ts">
  // The accounts the instance serves, under the setup checklist while
  // anything is missing.
  import HelpPopover from '../../components/HelpPopover.svelte';
  import { href } from '../../router.svelte';
  import { Resource } from '../../resource.svelte';
  import { tokens, costTotal, wholeNumber } from '../../format';
  import type { AdminAccount } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import Pill from '../../components/Pill.svelte';
  import Checklist from './Checklist.svelte';

  const res = Resource.json<AdminAccount[]>(() => '/api/v1/admin/accounts');
  $effect(() => {
    void res.load();
  });
</script>

{#if res.data}<Checklist accounts={res.data} />{/if}
<section class="panel" aria-labelledby="op-accounts">
  <header class="panel-head"><h2 id="op-accounts">Accounts</h2><HelpPopover label="About accounts"><p>Accounts served by a GitHub App, including configured accounts that are not yet connected.</p></HelpPopover></header>
  <StateView {res} retry={() => res.load()} isEmpty={(d) => d.length === 0} empty="No accounts yet: declare a GitHub App under apps in the configuration file.">
    {#snippet children(list)}
      <div class="table-wrap overflow-x-auto max-w-full">
        <table class="data">
          <thead>
            <tr>
              <th scope="col">Account</th>
              <th scope="col">App</th>
              <th scope="col">State</th>
              <th scope="col" class="num wrap">Enabled repos</th>
              <th scope="col" class="num">Reviews 7d</th>
              <th scope="col" class="num wrap">Tokens (month)</th>
              <th scope="col" class="num wrap">API spend (month)</th>
            </tr>
          </thead>
          <tbody>
            {#each list as t (t.slug)}
              <tr>
                <td class="mono name-fill" title={t.slug}>
                  {#if t.live}<a href={href({ name: 'account', slug: t.slug })}>{t.slug}</a>{:else}{t.slug}{/if}
                </td>
                <td class="mono small name-clip text-ui-sm" title={t.connection}>{t.connection || '—'}</td>
                <td>
                  {#if t.conflict}
                    <Pill tone="warn" label="not served" />
                    <span class="small muted text-ui-sm">{t.conflict}</span>
                  {:else}
                    <Pill tone="ok" label="live" />
                  {/if}
                </td>
                <td class="num">{wholeNumber(t.repositories)}</td>
                <td class="num">{wholeNumber(t.reviews7d)}</td>
                <td class="num">{tokens(t.usage.tokens)}</td>
                <td class="num">{costTotal(t.usage.costUsd, t.usage.unpricedCalls)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/snippet}
  </StateView>
</section>
