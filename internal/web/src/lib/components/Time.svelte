<script lang="ts">
  // An instant as dates.ts writes one: relative, or its date once it is over
  // a week off. The shared clock keeps it current. The full timestamp shows on
  // hover, and the ISO one is in the datetime attribute. `full` writes the full
  // timestamp instead, where instants seconds apart are read against each
  // other.
  import { ago, stamp } from '../time.svelte';
  let { iso, full = false }: { iso: string | null | undefined; full?: boolean } = $props();
</script>

{#if iso && full}
  <time class="reltime text-muted text-ui-sm whitespace-nowrap" datetime={iso}>{stamp(iso)}</time>
{:else if iso}
  <time class="reltime text-muted text-ui-sm whitespace-nowrap" datetime={iso} title={stamp(iso)}>{ago(iso)}</time>
{:else}
  <span class="muted">—</span>
{/if}
