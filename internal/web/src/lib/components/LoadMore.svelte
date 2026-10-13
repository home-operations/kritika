<script lang="ts" generics="T">
  import type { Paged } from '../resource.svelte';
  import { errorMessage } from '../resource.svelte';
  let { paged }: { paged: Paged<T> } = $props();
</script>

{#if paged.moreError}
  <p class="state-msg state-error flex items-center gap-2 m-0 py-3 px-4 text-danger border border-solid border-[color-mix(in_srgb,_var(--danger)_35%,_var(--border))] [background:color-mix(in_srgb,_var(--danger)_6%,_transparent)] rounded-md" role="alert">Loading more failed: {errorMessage(paged.moreError)}</p>
{/if}
{#if paged.cursor}
  <button class="btn load-more self-center" onclick={() => paged.more()} disabled={paged.loadingMore}>
    {paged.loadingMore ? 'Loading…' : 'Load more'}
  </button>
{/if}
