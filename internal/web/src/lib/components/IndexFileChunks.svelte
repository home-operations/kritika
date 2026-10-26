<script lang="ts">
  import { Resource } from '../resource.svelte';
  import type { IndexChunks } from '../types';
  import StateView from './StateView.svelte';

  let { base, path }: { base: string; path: string } = $props();
  const res = Resource.live<IndexChunks>(() => `${base}/index/chunks?path=${encodeURIComponent(path)}`);
  $effect(() => { void res.load(); });
  const lines = (s: number, e: number): string => (s === e ? `line ${s}` : `lines ${s}–${e}`);
</script>

<StateView {res} layout="inline" retry={() => res.load()}>
  {#snippet children(got)}
    <ol class="plain-list m-0 p-0 flex flex-col gap-2">
      {#each got.items as ch (`${ch.startLine}-${ch.endLine}-${ch.symbol}`)}
        <li class="min-w-0">
          <span class="small text-ui-sm">{lines(ch.startLine, ch.endLine)}</span>
          {#if ch.symbol}<span class="mono small text-ui-sm">{ch.kind ? `${ch.kind} ` : ''}{ch.scope ? `${ch.scope}.` : ''}{ch.symbol}</span>{/if}
          {#if ch.text}<pre class="mono text-ui-xs m-0 mt-1 p-2 bg-surface border border-solid border-line rounded-md overflow-x-auto max-h-60">{ch.text}</pre>{/if}
        </li>
      {/each}
    </ol>
    {#if got.truncated}<p class="small muted text-ui-sm m-0 mt-2">Only the first {got.items.length} chunks are shown.</p>{/if}
  {/snippet}
</StateView>
