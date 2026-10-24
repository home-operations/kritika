<script lang="ts">
  // Every user's personal access tokens, which an admin can revoke, for a
  // leaked token or someone leaving.
  import { Resource } from '../../resource.svelte';
  import type { APIToken } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import TokenTable from '../../components/TokenTable.svelte';

  const tokens = Resource.json<APIToken[]>(() => '/api/v1/admin/tokens');
  $effect(() => {
    void tokens.load();
  });
</script>

<section class="panel" aria-labelledby="op-tokens">
  <header class="panel-head"><h2 id="op-tokens">API tokens</h2></header>
  <StateView res={tokens} retry={() => tokens.load()} isEmpty={(d) => d.length === 0} empty="No one has created a token.">
    {#snippet children(list)}
      <TokenTable tokens={list} owners revokePath={(id) => `/api/v1/admin/tokens/${encodeURIComponent(id)}`} onrevoked={() => tokens.load()} fallback="#op-tokens" />
    {/snippet}
  </StateView>
</section>
