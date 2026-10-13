<script lang="ts">
  import { queueFilter, type QueueFilter } from '../routes';
  import { replace } from '../router.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  // The queue of every account the viewer can read, and the model slots
  // each account's running reviews hold, which is why a job of it waits.
  import { href } from '../router.svelte';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import type { InstanceQueue } from '../types';
  import StateView from '../components/StateView.svelte';
  import JobTable from '../components/JobTable.svelte';
  import Meter from '../components/Meter.svelte';

  let { filter }: { filter?: QueueFilter } = $props();

  const res = Resource.json<InstanceQueue>(() => '/api/v1/queue');

  $effect(() => {
    void res.load();
  });
  $effect(() => live(() => true, () => void res.load()));
  $effect(() => pollJobs(() => void res.load()));
</script>

<svelte:head><title>Queue · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Queue" description="Jobs across all your accounts." />
    <StateView {res} retry={() => res.load()}>
      {#snippet children(q)}
        <JobTable {filter} onfilter={(f) => replace({ name: 'instanceQueue', filter: queueFilter({ ...f }) })} ondone={() => void res.load()} jobs={q.jobs} account={(j) => j.account} several />
        {#if q.slots.length}
          <section class="panel" aria-labelledby="iq-slots">
            <header class="panel-head">
              <h2 id="iq-slots">Model slots</h2>
              <span class="small muted text-ui-sm">Reviews wait when all model slots are busy.</span>
            </header>
            <div class="table-wrap overflow-x-auto max-w-full">
              <table class="data">
                <thead>
                  <tr><th scope="col">Account</th><th scope="col">Model</th><th scope="col">Busy</th></tr>
                </thead>
                <tbody>
                  {#each q.slots as s (`${s.account}\u0000${s.model}`)}
                    <tr>
                      <td class="mono"><a href={href({ name: 'queue', slug: s.account })}>{s.account}</a></td>
                      <td class="mono small text-ui-sm">{s.model}</td>
                      <td>
                        <span class="small text-ui-sm">{s.slots ? `${s.held} of ${s.slots}` : `${s.held}, no limit`}</span>
                        {#if s.slots}<Meter value={s.held} max={s.slots} label={`Slots of ${s.model} busy for ${s.account}`} />{/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </section>
        {/if}

      {/snippet}
    </StateView>
  </div>
</main>
