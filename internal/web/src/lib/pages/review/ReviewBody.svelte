<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { href } from '../../router.svelte';
  import type { ReviewTab, Route } from '../../routes';
  import type { PullDetail, ReviewDetail } from '../../types';
  import { revealInNav } from '../../focus';
  import SummaryTab from './SummaryTab.svelte';
  import DiffTab from './DiffTab.svelte';
  import ConversationTab from './ConversationTab.svelte';
  import TimelineTab from './TimelineTab.svelte';
  import RawTab from './RawTab.svelte';
  import UsageTab from './UsageTab.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import ReviewImpact from './ReviewImpact.svelte';
  import ReviewMeta from '../../components/ReviewMeta.svelte';
  import ReviewStatusTile from '../../components/ReviewStatusTile.svelte';
  import FollowupItem from '../../components/FollowupItem.svelte';
  import Time from '../../components/Time.svelte';

  let { slug, d, pull, base, version, tab, finding, page, to }: {
    slug: string; d: ReviewDetail; pull?: PullDetail; base: string; version: number;
    tab: ReviewTab; finding?: string; page?: HTMLElement;
    to: (tab: ReviewTab, finding?: string) => Route;
  } = $props();

  let selectedFinding = $state('');
  let inspection = $state(0);
  let visited = $state<ReviewTab[]>([]);
  let shown: ReviewTab | undefined;
  const positions = new Map<ReviewTab, number>();
  // Visited views retain file expansion, filters, transcripts and scroll.
  $effect.pre(() => {
    const next = tab;
    untrack(() => {
      if (shown === next) return;
      if (shown && page) positions.set(shown, page.scrollTop);
      if (!visited.includes(next)) visited = [...visited, next];
      shown = next;
      void tick().then(() => { if (page) page.scrollTop = positions.get(next) ?? 0; });
    });
  });

  const primary: readonly ReviewTab[] = ['summary', 'diff', 'activity'];
  const diagnostics: readonly ReviewTab[] = ['timeline', 'conversation', 'usage', 'raw'];
  const inRun = $derived(diagnostics.includes(tab));
  const labels: Record<ReviewTab, string> = {
    summary: 'Overview', diff: 'Findings & code', activity: 'Activity',
    timeline: 'Timeline', conversation: 'Conversation', usage: 'Usage', raw: 'Raw',
  };
</script>

<nav class="tabs" aria-label="Review sections">
  {#each primary as t (t)}
    <a class="tab" class:active={t === tab} aria-current={t === tab ? 'page' : undefined}
      href={href(to(t, t === 'diff' ? selectedFinding || undefined : undefined))} {@attach t === tab && revealInNav}>
      <span class="tab-label" data-label={labels[t]}>{labels[t]}</span>
      {#if t === 'diff' && d.findings.length}<span class="tab-count" aria-hidden="true">{d.findings.length}</span>{/if}
    </a>
  {/each}
  <a class="tab run-details-tab" class:active={inRun} aria-current={inRun ? 'page' : undefined} href={href(to('timeline'))} {@attach inRun && revealInNav}><span class="tab-label" data-label="Run details">Run details</span></a>
</nav>

{#if inRun}
  <section class="run-details" aria-label="Run details">
    <ReviewMeta r={d.review} scopeReason={d.review.scopeReason} />
    <nav class="tabs" aria-label="Run sections">
      {#each diagnostics as t (t)}
        <a class="tab" class:active={t === tab} aria-current={t === tab ? 'page' : undefined} href={href(to(t))} {@attach t === tab && revealInNav}><span class="tab-label" data-label={labels[t]}>{labels[t]}</span></a>
      {/each}
    </nav>
  </section>
{/if}

{#if tab === 'diff' && d.summary}
  <section class="review-conclusion" aria-label="Review summary">
    <h2>{d.summary.headline || 'Review summary'}</h2>
    <Markdown text={d.summary.take} />
    {#if d.summary.impact?.length}<div class="merge-impact"><strong>Merge impact</strong><ReviewImpact notes={d.summary.impact} /></div>{/if}
  </section>
{/if}

<section class="tab-panel" aria-label={labels[tab]}>
  {#each visited as view (view)}
    <div class="review-tab" hidden={view !== tab}>
      {#if view === 'summary'}
        <SummaryTab {slug} {d} {to} oninspect={() => inspection++} finding={tab === view ? finding : undefined} />
      {:else if view === 'diff'}
        <DiffTab {base} {d} {version} {finding} {inspection} bind:selected={selectedFinding} active={tab === view} />
      {:else if view === 'activity'}
        {#if pull}
          <section class="panel" aria-labelledby="pull-reviews">
            <header class="panel-head"><h2 id="pull-reviews">Review history</h2></header>
            <ol class="timeline">
              {#each pull.reviews as r (r.id)}
                <li class="timeline-item">
                  <a class="timeline-link" href={href({ name: 'review', slug, id: r.id })}>
                    <span class="timeline-top"><ReviewStatusTile status={r.status} /><Time iso={r.createdAt} /></span>
                    <ReviewMeta {r} />
                    {#if r.error}<span class="error-text">{r.error}</span>{/if}
                  </a>
                </li>
              {/each}
            </ol>
          </section>
          <section class="panel" aria-labelledby="pull-followups">
            <header class="panel-head"><h2 id="pull-followups">Follow-ups</h2></header>
            {#if pull.followups.length}
              <ul class="followups">{#each pull.followups as f (f.id)}<FollowupItem {slug} {f} />{/each}</ul>
            {:else}<p class="state-msg">No follow-up questions.</p>{/if}
          </section>
        {:else}<p class="state-msg">Pull request activity is unavailable until its details load.</p>{/if}
      {:else if view === 'conversation'}
        <ConversationTab active={tab === view} {base} {version} {slug} />
      {:else if view === 'timeline'}
        <TimelineTab {slug} {d} />
      {:else if view === 'raw'}
        <RawTab active={tab === view} {base} {d} {version} />
      {:else}
        <UsageTab {d} />
      {/if}
    </div>
  {/each}
</section>
