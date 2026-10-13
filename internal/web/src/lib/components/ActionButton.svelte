<script lang="ts">
  import type { ComponentProps } from 'svelte';
  import ActionDialog from './ActionDialog.svelte';

  let props: Omit<ComponentProps<typeof ActionDialog>, 'open'> = $props();
  let open = $state(false);
</script>

<button class="btn" class:btn-danger={props.danger} onclick={() => (open = true)}>{props.label}</button>
<ActionDialog {...props} bind:open />
