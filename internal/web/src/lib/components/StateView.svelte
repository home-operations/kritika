<script lang="ts" generics="T">
  // The loading / error / empty / loaded switch every page shares. Keeps the
  // previous data on screen during a background refetch so a live update
  // doesn't flash a spinner.
  import type { Snippet } from 'svelte';
  import type { Resource } from '../resource.svelte';
  import { errorMessage } from '../resource.svelte';

  interface Props {
    res: Resource<T>;
    children: Snippet<[T]>;
    isEmpty?: (data: T) => boolean;
    empty?: string;
    retry?: () => void;
    layout?: 'table' | 'overview' | 'account';
  }
  let { res, children, isEmpty, empty = 'Nothing here yet.', retry, layout = 'table' }: Props = $props();
</script>

<span hidden data-loading={res.loading || (res.data === undefined && !res.error)}></span>

{#if res.data !== undefined}
  {#if res.error}
    <div class="state-msg state-error" role="alert">
      <span>Refresh failed: {errorMessage(res.error)}</span>
      {#if retry}<button class="btn" onclick={retry}>Retry</button>{/if}
    </div>
  {/if}
  {#if isEmpty?.(res.data)}
    <p class="state-msg">{empty}</p>
  {:else}
    {@render children(res.data)}
  {/if}
{:else if res.error}
  <div class="state-msg state-error" role="alert">
    <span>{errorMessage(res.error)}</span>
    {#if retry}<button class="btn" onclick={retry}>Retry</button>{/if}
  </div>
{:else}
  <div class="loading-placeholder" class:loading-overview={layout !== 'table'} role="status" aria-label="Loading content" aria-busy="true">
    <span class="sr-only">Loading…</span>
    {#if layout !== 'table'}<div class="tiles" class:overview-totals={layout === 'overview'} aria-hidden="true">{#each (layout === 'overview' ? [1, 2, 3, 4, 5, 6] : [1, 2, 3, 4]) as tile (tile)}<div class="tile skeleton-tile"><span class="skeleton-line"></span><span class="skeleton-value"></span><span class="skeleton-line"></span></div>{/each}</div>{/if}
    <div class="skeleton-table" aria-hidden="true">{#each [1, 2, 3, 4, 5] as row (row)}<div class="skeleton-row"><span class="skeleton-line"></span><span class="skeleton-line"></span><span class="skeleton-line"></span></div>{/each}</div>
  </div>
{/if}
