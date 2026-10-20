<script lang="ts">
  import { queueFilter, type QueueFilter } from '../routes';
  import { replace } from '../router.svelte';
  import { Resource, pollJobs } from '../resource.svelte';
  import { accountApi } from '../links';
  import type { Job } from '../types';
  import StateView from '../components/StateView.svelte';
  import JobTable from '../components/JobTable.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import QueueHelp from '../components/QueueHelp.svelte';

  let { slug, filter }: { slug: string; filter?: QueueFilter } = $props();
  const res = Resource.live<Job[]>(() => `${accountApi(slug)}/queue`);

  $effect(() => {
    void res.load();
  });
  $effect(() => pollJobs(() => void res.load()));
</script>

<svelte:head><title>Queue · {slug} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Queue" {slug}>{#snippet actions()}<QueueHelp />{/snippet}</PageHeader>
    <StateView {res} retry={() => res.load()}>
      {#snippet children(jobs)}
        <JobTable {filter} onfilter={(f) => replace({ name: 'queue', slug, filter: queueFilter({ ...f }) })} ondone={() => void res.load()} {jobs} account={() => slug} />
      {/snippet}
    </StateView>
  </div>
</main>
