<script lang="ts">
  // A usage-vs-limit bar, on Bits UI's meter. A limit of 0 means unlimited:
  // no bar, just "no cap".
  import { Meter } from 'bits-ui';
  let { value, max, label }: { value: number; max: number; label: string } = $props();
  const pct = $derived(max > 0 ? Math.min(100, (value / max) * 100) : 0);
  const tone = $derived(pct >= 90 ? 'danger' : pct >= 70 ? 'warn' : 'ok');
</script>

{#if max > 0}
  <Meter.Root class="meter tone-{tone}" aria-label={label} {value} {max}>
    <span class="meter-fill" style:width="{pct}%"></span>
  </Meter.Root>
{:else}
  <span class="muted small">no cap</span>
{/if}
