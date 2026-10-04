<script lang="ts">
  // An instant as dates.ts writes one: relative, or its date once it is over
  // a week off, current off the shared clock, with the full timestamp on
  // hover and the ISO one in the datetime attribute.
  import { clock } from '../time.svelte';
  import { relative, timestamp } from '../dates';
  let { iso }: { iso: string | null | undefined } = $props();
  // Reading rev has both written again when the viewer's zone or clock changes.
  const title = $derived((clock.rev, timestamp(iso)));
  const text = $derived((clock.rev, relative(iso, clock.now)));
</script>

{#if iso}
  <time class="reltime" datetime={iso} {title}>{text}</time>
{:else}
  <span class="muted">—</span>
{/if}
