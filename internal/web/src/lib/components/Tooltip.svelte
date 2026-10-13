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
    <Tooltip.Content role="tooltip" class="hint z-[calc(var(--z-modal)_+_1)] max-w-[min(300px,_calc(100vw_-_16px))] py-1.5 px-2.5 text-ink bg-panel border border-solid border-line rounded-lg shadow-[0_6px_18px_color-mix(in_srgb,_var(--text)_12%,_transparent)] text-ui-sm" sideOffset={6} collisionPadding={8}>{text}</Tooltip.Content>
  </Tooltip.Portal>
</Tooltip.Root>
