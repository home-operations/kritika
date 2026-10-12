<script lang="ts">
  import { Popover } from 'bits-ui';
  import type { Snippet } from 'svelte';

  let { label, children }: { label: string; children: Snippet } = $props();
</script>

<Popover.Root>
  <Popover.Trigger class="btn btn-quiet help-trigger">{label}</Popover.Trigger>
  <Popover.Portal>
    <Popover.Content class="menu-panel help-popover" sideOffset={8} collisionPadding={12} role="dialog" aria-label={label}>
      <h2>{label}</h2>
      {@render children()}
    </Popover.Content>
  </Popover.Portal>
</Popover.Root>
