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
    layout?: 'table' | 'overview' | 'inline';
  }
  let { res, children, isEmpty, empty = 'Nothing here yet.', retry, layout = 'table' }: Props = $props();
</script>

<span hidden data-loading={res.loading || (res.data === undefined && !res.error)}></span>

{#if res.data !== undefined}
  {#if res.error}
    <div class="state-msg state-error flex items-center gap-2 m-0 py-3 px-4 text-danger border border-solid border-[color-mix(in_srgb,_var(--danger)_35%,_var(--border))] [background:color-mix(in_srgb,_var(--danger)_6%,_transparent)] rounded-md" role="alert">
      <span>Refresh failed: {errorMessage(res.error)}</span>
      {#if retry}<button class="btn" onclick={retry}>Retry</button>{/if}
    </div>
  {/if}
  {#if isEmpty?.(res.data)}
    <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">{empty}</p>
  {:else}
    {@render children(res.data)}
  {/if}
{:else if res.error}
  <div class="state-msg state-error flex items-center gap-2 m-0 py-3 px-4 text-danger border border-solid border-[color-mix(in_srgb,_var(--danger)_35%,_var(--border))] [background:color-mix(in_srgb,_var(--danger)_6%,_transparent)] rounded-md" role="alert">
    <span>{errorMessage(res.error)}</span>
    {#if retry}<button class="btn" onclick={retry}>Retry</button>{/if}
  </div>
{:else if layout === 'inline'}
  <p class="muted" role="status">Loading…</p>
{:else}
  <div class="loading-placeholder w-full" class:loading-overview={layout !== 'table'} role="status" aria-label="Loading content" aria-busy="true">
    <span class="sr-only">Loading…</span>
    {#if layout === 'overview'}<div class="stats stats-3" aria-hidden="true">{#each [1, 2, 3] as tile (tile)}<div class="stat skeleton-tile gap-4"><span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span><span class="skeleton-value block bg-line rounded-[4px] w-14 h-9"></span><span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span></div>{/each}</div>{/if}
    <div class="skeleton-table border border-solid border-line rounded-md overflow-hidden" aria-hidden="true">{#each [1, 2, 3, 4, 5] as row (row)}<div class="skeleton-row grid grid-cols-[3fr_1fr_1fr] gap-6 py-6 px-4 border-b border-solid border-b-line"><span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span><span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span><span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span></div>{/each}</div>
  </div>
{/if}
