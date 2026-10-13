<script lang="ts">
  import Icon from '../Icon.svelte';
  import { mdiChevronDown, mdiChevronRight } from '../icons';

  let { open = $bindable(), label, controls }: { open?: boolean; label: string; controls: string } = $props();
</script>

<button class="btn btn-quiet row-toggle" aria-label={label} aria-expanded={!!open} aria-controls={controls} onclick={() => { open = !open; }}>
  <Icon path={open ? mdiChevronDown : mdiChevronRight} size={18} />
</button>
