<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { accountApi } from '../links';
  import { href } from '../router.svelte';
  import { Resource, live } from '../resource.svelte';
  import type { Page, Pull } from '../types';
  import PullTable from './PullTable.svelte';
  import StateView from './StateView.svelte';

  let { slug }: { slug: string } = $props();
  const res = new Resource(() => getJSON<Page<Pull>>(`${accountApi(slug)}/pulls?state=all&limit=5`));
  $effect(() => {
    void res.load();
  });
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' || e.kind === 'followup'), () => void res.load()));
</script>

<section class="recent-pulls" aria-labelledby="recent-pulls-title">
  <header class="panel-head"><h2 id="recent-pulls-title">Recent pull requests</h2><a href={href({ name: 'pulls', slug })}>View all →</a></header>
  <StateView {res} retry={() => res.load()} isEmpty={(d) => !d.items.length} empty="No pull requests yet.">
    {#snippet children(d)}<PullTable {slug} items={d.items} />{/snippet}
  </StateView>
</section>
