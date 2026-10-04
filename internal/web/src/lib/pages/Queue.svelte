<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import { accountApi } from '../links';
  import type { Job } from '../types';
  import StateView from '../components/StateView.svelte';
  import JobTable from '../components/JobTable.svelte';
  import SectionTabs from '../components/SectionTabs.svelte';

  let { slug }: { slug: string } = $props();
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
    <SectionTabs section="pulls" {slug} current="queue" />
    <StateView {res} retry={() => res.load()} isEmpty={(d) => d.length === 0} empty="The queue is empty.">
      {#snippet children(jobs)}
        <JobTable {jobs} account={() => slug} />
      {/snippet}
    </StateView>
  </div>
</main>
