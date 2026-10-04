<script lang="ts">
  // The queue of every account the viewer can read, and the model slots
  // each account's running reviews hold, which is why a job of it waits.
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { Resource, live, pollJobs } from '../resource.svelte';
  import type { InstanceQueue } from '../types';
  import StateView from '../components/StateView.svelte';
  import JobTable from '../components/JobTable.svelte';
  import Meter from '../components/Meter.svelte';

  const res = new Resource(() => getJSON<InstanceQueue>('/api/v1/queue'));

  $effect(() => {
    void res.load();
  });
  $effect(() => live(() => true, () => void res.load()));
  $effect(() => pollJobs(() => void res.load()));
</script>

<svelte:head><title>Queue · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <header class="page-head">
      <h1>Queue</h1>
      <p class="muted small">The review, follow-up and index jobs of every account you can read.</p>
    </header>
    <StateView {res} retry={() => res.load()}>
      {#snippet children(q)}
        {#if q.slots.length}
          <section class="panel" aria-labelledby="iq-slots">
            <header class="panel-head">
              <h2 id="iq-slots">Model slots</h2>
              <span class="small muted">A review waits while every slot of its account's model is busy.</span>
            </header>
            <div class="table-wrap">
              <table class="data">
                <thead>
                  <tr><th scope="col">Account</th><th scope="col">Model</th><th scope="col">Busy</th></tr>
                </thead>
                <tbody>
                  {#each q.slots as s (`${s.account}\u0000${s.model}`)}
                    <tr>
                      <td class="mono"><a href={href({ name: 'queue', slug: s.account })}>{s.account}</a></td>
                      <td class="mono small">{s.model}</td>
                      <td>
                        <span class="small">{s.slots ? `${s.held} of ${s.slots}` : `${s.held}, no limit`}</span>
                        {#if s.slots}<Meter value={s.held} max={s.slots} label={`Slots of ${s.model} busy for ${s.account}`} />{/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </section>
        {/if}
        {#if q.jobs.length === 0}
          <p class="state-msg">The queue is empty.</p>
        {:else}
          <JobTable jobs={q.jobs} account={(j) => j.account} several />
        {/if}
      {/snippet}
    </StateView>
  </div>
</main>
