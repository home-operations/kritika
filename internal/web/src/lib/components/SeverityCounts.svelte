<script lang="ts">
  import type { SeverityCounts } from '../types';
  import { SEVERITIES } from '../format';
  let { counts }: { counts: SeverityCounts } = $props();
</script>

<span class="sev-counts inline-flex gap-1">
  {#each SEVERITIES as s (s)}
    {#if counts[s] > 0}
      <span class="sev-chip sev-{s}" title="{counts[s]} {s}">{counts[s]}<span class="sev-count-label uppercase">{` ${s} `}</span></span>
    {/if}
  {/each}
</span>
