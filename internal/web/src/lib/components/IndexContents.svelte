<script lang="ts">
  // What a repository's similar-code index holds: the generation's commit
  // and embedder, its counts, and its files, each opening onto its chunks.
  // A chunk's text comes to an admin alone; the server leaves it out for
  // anyone else.
  import { Paged, Resource } from '../resource.svelte';
  import { getJSON } from '../api.svelte';
  import { describe } from '../manage';
  import { shortSha, wholeNumber } from '../format';
  import type { IndexChunks, IndexContents, IndexFile } from '../types';
  import StateView from './StateView.svelte';
  import LoadMore from './LoadMore.svelte';
  import RowToggle from './RowToggle.svelte';
  import Time from './Time.svelte';
  import Pill from './Pill.svelte';

  let { base }: { base: string } = $props();
  const tableId = $props.id();

  const summary = Resource.live<IndexContents>(() => `${base}/index`);
  $effect(() => {
    void summary.load();
  });

  // query is the path filter as typed; q follows it once typing pauses.
  let query = $state('');
  let q = $state('');
  $effect(() => {
    const typed = query.trim();
    const t = setTimeout(() => (q = typed), 250);
    return () => clearTimeout(t);
  });
  const files = new Paged<IndexFile>(
    (cursor) => `${base}/index/files?limit=100${q ? `&q=${encodeURIComponent(q)}` : ''}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
    (f) => f.path,
  );
  $effect(() => {
    void q;
    void files.load();
  });

  let open = $state<Record<string, boolean>>({});
  let chunks = $state<Record<string, IndexChunks | string>>({});
  // show opens or closes a file's chunks, fetching them the first time.
  async function show(path: string, on: boolean): Promise<void> {
    open[path] = on;
    if (!on || chunks[path]) return;
    try {
      chunks[path] = await getJSON<IndexChunks>(`${base}/index/chunks?path=${encodeURIComponent(path)}`);
    } catch (err) {
      chunks[path] = describe(err);
    }
  }
  const lines = (s: number, e: number): string => (s === e ? `line ${s}` : `lines ${s}–${e}`);
</script>

<StateView res={summary} retry={() => summary.load()}>
  {#snippet children(c)}
    {#if !c.indexed}
      <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">Nothing is indexed yet.</p>
    {:else}
      <dl class="deflist grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-3 mx-4 text-ui-sm">
        <dt>Commit</dt><dd class="mono" title={c.commit}>{shortSha(c.commit)}</dd>
        <dt>Embedder</dt>
        <dd>
          <span class="mono">{c.embedModel}</span>, {wholeNumber(c.embedDims)} dimensions
          {#if !c.current}<Pill tone="warn" label="stale" title="Built by another embedder than the configured one: similar-code lookups skip it until it is rebuilt." />{/if}
        </dd>
        <dt>Files</dt><dd>{wholeNumber(c.files)}</dd>
        <dt>Chunks</dt><dd>{wholeNumber(c.chunks)}</dd>
        {#if c.builtAt}<dt>Built</dt><dd><Time iso={c.builtAt} /></dd>{/if}
      </dl>
      <div class="toolbar flex flex-wrap items-center gap-2 mx-4 mb-2">
        <label class="search-box [flex:1_1_240px] min-w-0 flex">
          <span class="sr-only">Filter indexed files</span>
          <input type="search" placeholder="Filter by path" bind:value={query} />
        </label>
      </div>
      <StateView res={files.first} retry={() => files.load()} isEmpty={(p) => p.items.length === 0} empty={q ? `No indexed file matches “${q}”.` : 'No files.'}>
        <div class="table-wrap overflow-x-auto max-w-full">
          <table class="data">
            <thead>
              <tr><th scope="col">Path</th><th scope="col">Language</th><th scope="col" class="num">Chunks</th><th scope="col" class="relative"><span class="sr-only">Chunks</span></th></tr>
            </thead>
            <tbody>
              {#each files.items as f, i (f.path)}
                {@const panelId = `${tableId}-${i}`}
                <tr>
                  <td class="mono small text-ui-sm">{f.path}</td>
                  <td>{f.language || '—'}</td>
                  <td class="num">{wholeNumber(f.chunks)}</td>
                  <td class="row-toggle-cell"><RowToggle bind:open={() => !!open[f.path], (on) => void show(f.path, !!on)} label={`Chunks of ${f.path}`} controls={panelId} /></td>
                </tr>
                {#if open[f.path]}
                  {@const got = chunks[f.path]}
                  <tr class="row-preview"><td colspan="4">
                    <div class="row-preview-body" id={panelId} role="region" aria-label={`Chunks of ${f.path}`}>
                      {#if got === undefined}
                        <p class="state-msg m-0 py-2 text-muted">Loading…</p>
                      {:else if typeof got === 'string'}
                        <p class="state-msg m-0 py-2 text-danger" role="alert">Loading the chunks failed: {got}</p>
                      {:else}
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
                      {/if}
                    </div>
                  </td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
        <LoadMore paged={files} />
      </StateView>
    {/if}
  {/snippet}
</StateView>
