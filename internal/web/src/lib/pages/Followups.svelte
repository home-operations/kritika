<script lang="ts">
  import { accountApi } from '../links';
  import { Paged, live } from '../resource.svelte';
  import type { Followup } from '../types';
  import StateView from '../components/StateView.svelte';
  import FollowupItem from '../components/FollowupItem.svelte';
  import LoadMore from '../components/LoadMore.svelte';
  import PageHeader from '../components/PageHeader.svelte';

  let { slug }: { slug: string } = $props();
  const base = $derived(`${accountApi(slug)}/followups`);
  const paged = new Paged<Followup>(
    (cursor) => `${base}?limit=50${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
    (f) => f.id,
  );
  const res = paged.first;

  $effect(() => {
    void paged.load();
  });
  $effect(() => live((e) => e.account === slug && e.kind === 'followup', () => void paged.refresh()));
</script>

<svelte:head><title>Follow-ups · {slug} · kritika</title></svelte:head>

<main class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Follow-ups" {slug} />
    <StateView {res} retry={() => paged.load()} isEmpty={(d) => d.items.length === 0} empty="No follow-up questions yet.">
      {#snippet children()}
        <ul class="followups list-none m-0 p-0">
          {#each paged.items as f (f.id)}<FollowupItem {slug} {f} showPull />{/each}
        </ul>
        <LoadMore {paged} />
      {/snippet}
    </StateView>
  </div>
</main>
