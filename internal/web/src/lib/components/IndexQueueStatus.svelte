<script lang="ts">
  // Where a repository stands for its next index: its index job queued or
  // running, or its place among the repositories waiting for the
  // onboarding feeder, which queues only a few first indexes at a time.
  import Pill from './Pill.svelte';
  import { indexJobText, jobTone } from '../format';
  import type { IndexState } from '../types';

  let { index }: { index: IndexState } = $props();
</script>

{#if index.job}
  <Pill tone={jobTone[index.job.state]} label={indexJobText(index.job)} title={`Index job #${index.job.id}`} />
{:else if index.backlog}
  <Pill
    label={`Waiting · #${index.backlog}`}
    title={`Waiting to be queued: onboarding queues a few first indexes at a time, and this repository is number ${index.backlog} in line.`}
  />
{/if}
