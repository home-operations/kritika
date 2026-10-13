<script lang="ts">
  import { getJSON } from '../../api.svelte';
  import { Resource } from '../../resource.svelte';
  import { pretty, bytes } from '../../format';
  import type { ReviewDetail, ReviewRaw } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import CodeBlock from '../../components/CodeBlock.svelte';
  import Collapsible from '../../components/Collapsible.svelte';

  let { active, base, d, version }: { active: boolean; base: string; d: ReviewDetail; version: number } = $props();
  const res = new Resource(() => getJSON<ReviewRaw>(`${base}/raw`));
  $effect(() => {
    void version;
    if (active) void res.load();
  });
  const pack = $derived(d.contextPack);
</script>

<StateView {res} retry={() => res.load()}>
  {#snippet children(x)}
    <section class="panel bg-panel border border-solid border-line rounded-lg min-w-0" aria-labelledby="raw-result">
      <header class="panel-head flex items-center justify-between border-b border-solid border-b-line py-3 px-4 gap-3"><h2 id="raw-result">Submitted result</h2></header>
      {#if x.result === null}<p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">Nothing was submitted.</p>{:else}<CodeBlock text={pretty(x.result)} label="result.json" />{/if}
    </section>

    <section class="panel bg-panel border border-solid border-line rounded-lg min-w-0" aria-labelledby="raw-log">
      <header class="panel-head flex items-center justify-between border-b border-solid border-b-line py-3 px-4 gap-3"><h2 id="raw-log">Log tail</h2></header>
      {#if x.logTail}<CodeBlock text={x.logTail} label="runner log" maxLines={80} />{:else}<p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">No log captured.</p>{/if}
    </section>

    <section class="panel bg-panel border border-solid border-line rounded-lg min-w-0" aria-labelledby="raw-pack">
      <header class="panel-head flex items-center justify-between border-b border-solid border-b-line py-3 px-4 gap-3"><h2 id="raw-pack">Context pack</h2></header>
      {#if pack}
        <dl class="deflist grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-2.5 mx-3 text-ui-sm">
          <dt>Head / base</dt><dd class="mono">{pack.headSha} / {pack.baseSha}</dd>
          <dt>Patch id</dt><dd class="mono">{pack.patchId}</dd>
          {#if pack.priorHeadSha}<dt>Prior head</dt><dd class="mono">{pack.priorHeadSha}</dd>{/if}
          <dt>Changed paths</dt><dd class="mono small text-ui-sm">{pack.changedPaths.join(', ') || '—'}</dd>
          {#if pack.deltaPaths.length}<dt>Delta paths</dt><dd class="mono small text-ui-sm">{pack.deltaPaths.join(', ')}</dd>{/if}
        </dl>
        {#if pack.repoNotes.length}
          <h3 class="subhead mt-3 mx-3 mb-1 text-ui-sm font-[650] text-muted">Notes</h3>
          <ul class="plain-list m-0 pl-4">{#each pack.repoNotes as n, i (i)}<li class="small text-ui-sm">{n}</li>{/each}</ul>
        {/if}
      {/if}
      <h3 class="subhead mt-3 mx-3 mb-1 text-ui-sm font-[650] text-muted">Stages ({x.stages.length})</h3>
      {#each x.stages as s, i (i)}
        <Collapsible title="{s.stage} · {s.path}:{s.startLine}-{s.endLine}">
          {#snippet meta()}<span class="mono small muted text-ui-sm">{s.kind} {s.symbol}</span>{/snippet}
          <CodeBlock text={s.text} label={s.language || undefined} lang={s.language || s.path} />
        </Collapsible>
      {:else}
        <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">No stages.</p>
      {/each}
    </section>

    <section class="panel bg-panel border border-solid border-line rounded-lg min-w-0" aria-labelledby="raw-files">
      <header class="panel-head flex items-center justify-between border-b border-solid border-b-line py-3 px-4 gap-3"><h2 id="raw-files">Repository files</h2></header>
      {#each Object.entries(x.repoFiles) as [path, text] (path)}
        <Collapsible title={path}>
          {#snippet meta()}<span class="small muted text-ui-sm">{bytes(text.length)}</span>{/snippet}
          <CodeBlock {text} label={path} />
        </Collapsible>
      {:else}
        <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">No repository files were read.</p>
      {/each}
    </section>
  {/snippet}
</StateView>
