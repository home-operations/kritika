<script lang="ts">
  import type { ComponentProps } from 'svelte';
  import ActionDialog from './ActionDialog.svelte';

  let props: Omit<ComponentProps<typeof ActionDialog>, 'open'> = $props();
  let open = $state(false);
</script>

<button class="btn border border-solid border-line bg-surface text-ink rounded-md py-1.5 px-3 text-ui-md cursor-pointer inline-flex items-center gap-1.5" class:btn-danger={props.danger} onclick={() => (open = true)}>{props.label}</button>
<ActionDialog {...props} bind:open />
