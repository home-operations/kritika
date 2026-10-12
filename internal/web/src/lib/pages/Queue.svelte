<script lang="ts">
  import { queueFilter, type QueueFilter } from '../routes';
  import { replace } from '../router.svelte';
  import { getJSON } from '../api.svelte';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import { accountApi } from '../links';
  import type { Job } from '../types';
  import StateView from '../components/StateView.svelte';
  import JobTable from '../components/JobTable.svelte';
  import PageHeader from '../components/PageHeader.svelte';

  let { slug, filter }: { slug: string; filter?: QueueFilter } = $props();
  const res = new Resource(() => getJSON<Job[]>(`${accountApi(slug)}/queue`));

  $effect(() => {
    void res.load();
  });
  $effect(() => live((e) => e.account === slug, () => void res.load()));
  $effect(() => pollJobs(() => void res.load()));
</script>

<svelte:head><title>Queue · {slug} · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <PageHeader title="Queue" {slug} />
    <StateView {res} retry={() => res.load()}>
      {#snippet children(jobs)}
        <JobTable {filter} onfilter={(f) => replace({ name: 'queue', slug, filter: queueFilter({ ...f }) })} ondone={() => void res.load()} {jobs} account={() => slug} />
      {/snippet}
    </StateView>
  </div>
</main>
