<script lang="ts">
  import { Popover } from 'bits-ui';
  import type { Snippet } from 'svelte';

  let { label, children }: { label: string; children: Snippet } = $props();
</script>

<Popover.Root>
  <Popover.Trigger class="btn btn-quiet help-trigger border border-solid rounded-md py-1.5 px-3 text-ui-md cursor-pointer inline-flex items-center gap-1.5 bg-transparent border-transparent text-muted">{label}</Popover.Trigger>
  <Popover.Portal>
    <Popover.Content class="menu-panel help-popover flex flex-col bg-panel border border-solid border-line rounded-[10px] shadow-[0_8px_24px_color-mix(in_srgb,_var(--text)_15%,_transparent)] w-[min(380px,_calc(100vw_-_24px))] p-4 text-ui-sm leading-[1.6] z-[var(--z-modal)]" sideOffset={8} collisionPadding={12} role="dialog" aria-label={label}>
      <h2>{label}</h2>
      {@render children()}
    </Popover.Content>
  </Popover.Portal>
</Popover.Root>
