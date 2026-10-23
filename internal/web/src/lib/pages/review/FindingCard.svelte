<script lang="ts">
  import type { Finding } from '../../types';
  import Markdown from '../../components/Markdown.svelte';
  import CodeBlock from '../../components/CodeBlock.svelte';
  import Collapsible from '../../components/Collapsible.svelte';
  import Icon from '../../Icon.svelte';
  import { mdiOpenInNew } from '../../icons';
  import { threadUrl, forgeName } from '../../links';
  import Reactions from '../../components/Reactions.svelte';
  import Pill from '../../components/Pill.svelte';

  // pullUrl is the pull request on the forge, where an inline finding's
  // thread is, and slug the account, whose forge anchors the thread.
  let { f, slug, pullUrl = '', compact = false, id }: { f: Finding; slug: string; pullUrl?: string; compact?: boolean; id?: string } = $props();
  const thread = $derived(pullUrl ? threadUrl(slug, pullUrl, f.forgeCommentId) : undefined);
  const where = $derived(f.endLine > f.line ? `${f.path}:${f.line}-${f.endLine}` : `${f.path}:${f.line}`);
</script>

<article {id} class="finding sev-edge-{f.severity}" aria-label="{f.severity}: {f.title}">
  <header class="finding-head flex flex-wrap items-center gap-y-1.5 gap-x-2.5">
    <span class="sev sev-{f.severity}">{f.severity}</span>
    {#if f.category}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="What kind of problem it is">{f.category}</span>{/if}
    <span class="finding-title font-semibold text-[length:16px]">{f.title}</span>
    {#if f.status === 'addressed'}
      <Pill tone="ok" label="addressed" title="A later review of the pull request no longer reports it" />
    {:else if f.status === 'dismissed'}
      <Pill label="dismissed" title="A maintainer dismissed it in its thread" />
    {/if}
    {#each f.rules as id (id)}<span class="badge mono inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Enforces the review rule {id}">{id}</span>{/each}
    {#each f.agents as name (name)}<span class="badge mono inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Raised by the {name} agent">{name}</span>{/each}
    {#if !compact}<span class="mono small muted text-ui-sm">{where}</span>{/if}
    <Reactions up={f.reactionsUp} down={f.reactionsDown} />
    {#if thread}
      <a class="external small finding-thread text-ui-sm ml-auto" href={thread} target="_blank" rel="noopener noreferrer">Thread on {forgeName(slug)} <Icon path={mdiOpenInNew} size={12} /></a>
    {:else if f.postedInline}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Posted as an inline comment on {forgeName(slug)}">inline</span>{/if}
  </header>
  {#if f.status === 'dismissed' && f.dismissReason}<p class="small muted text-ui-sm">Dismissed: {f.dismissReason}</p>{/if}
  {#if f.explanation}<Markdown text={f.explanation} />{/if}
  {#if f.suggestedFix}
    <p class="finding-label m-0 text-ui-sm font-semibold text-muted">Suggested fix</p>
    <Markdown text={f.suggestedFix} />
  {/if}
  {#if f.replacement}<CodeBlock text={f.replacement} label="replacement" lang={f.path} />{/if}
  {#if f.agentPrompt}
    <Collapsible title="Agent prompt"><CodeBlock text={f.agentPrompt} plain /></Collapsible>
  {/if}
</article>
