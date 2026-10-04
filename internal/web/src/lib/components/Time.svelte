<script lang="ts">
  // An instant as dates.ts writes one: relative, or its date once it is over
  // a week off, current off the shared clock, with the full timestamp on
  // hover and the ISO one in the datetime attribute.
  import { ago, stamp } from '../time.svelte';
  let { iso }: { iso: string | null | undefined } = $props();
</script>

{#if iso}
  <time class="reltime" datetime={iso} title={stamp(iso)}>{ago(iso)}</time>
{:else}
  <span class="muted">—</span>
{/if}
