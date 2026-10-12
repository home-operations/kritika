<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Tooltip } from 'bits-ui';

  let { text, children, ...props }: Omit<Tooltip.TriggerProps, 'child' | 'children' | 'ref'> & { text: string; children: Snippet } = $props();
  let trigger = $state<HTMLButtonElement | null>(null);
  // Keep portaled hints inside their landmark without clipping them to cards.
  const target = $derived(trigger?.closest('main, header, [role="dialog"], [role="alertdialog"]') ?? undefined);
</script>

<Tooltip.Root>
  <Tooltip.Trigger {...props} bind:ref={trigger}>{@render children()}</Tooltip.Trigger>
  <Tooltip.Portal to={target}>
    <Tooltip.Content role="tooltip" class="hint" sideOffset={6} collisionPadding={8}>{text}</Tooltip.Content>
  </Tooltip.Portal>
</Tooltip.Root>
