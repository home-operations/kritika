<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { getJSON } from '../../api.svelte';
  import { revealInNav } from '../../focus';
  import { href } from '../../router.svelte';
  import { REVIEW_TABS, type ReviewTab } from '../../routes';
  import { Resource, live } from '../../resource.svelte';
  import { isActive, rerunBody, skipText } from '../../format';
  import { pullRoute, rerunPath, cancelPath, accountApi } from '../../links';
  import { isAdmin } from '../../session.svelte';
  import ActionButton from '../../components/ActionButton.svelte';
  import type { ReviewDetail } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import ReviewStatusTile from '../../components/ReviewStatusTile.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import ReviewImpact from './ReviewImpact.svelte';
  import ReviewMeta from '../../components/ReviewMeta.svelte';
  import Time from '../../components/Time.svelte';
  import NoMoreReviews from '../../components/NoMoreReviews.svelte';
  import SummaryTab from './SummaryTab.svelte';
  import DiffTab from './DiffTab.svelte';
  import ConversationTab from './ConversationTab.svelte';
  import TimelineTab from './TimelineTab.svelte';
  import RawTab from './RawTab.svelte';
  import UsageTab from './UsageTab.svelte';

  let { slug, id, tab = 'summary', finding }: { slug: string; id: string; tab?: ReviewTab; finding?: string } = $props();

  let page: HTMLElement;
  let selectedFinding = $state('');
  // Inspect links refocus a finding; returning to its tab preserves scroll.
  let inspection = $state(0);
  let visited = $state<ReviewTab[]>([]);
  let shown: ReviewTab | undefined;
  const positions = new Map<ReviewTab, number>();
  // Keep visited tabs mounted: file expansion, filters, and loaded transcripts
  // must survive a trip to another tab of this review.
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

  const base = $derived(`${accountApi(slug)}/reviews/${encodeURIComponent(id)}`);
  const res = new Resource(() => getJSON<ReviewDetail>(base));
  // Bumped on every live refresh so the lazily-loaded tab data refetches too.
  let version = $state(0);

  $effect(() => {
    void res.load();
  });
  $effect(() =>
    live(
      (e) => e.account === slug && e.reviewId === id && !!res.data && isActive(res.data.review.status),
      () => {
        void res.load();
        version++;
      },
    ),
  );

  // The status leads, so a tab left open on a running review shows when it
  // is done.
  const title = $derived.by(() => {
    const r = res.data?.review;
    return r ? `${r.status} · ${r.pull.repository}#${r.pull.number} review` : 'Review';
  });

  const RUN_TABS: readonly ReviewTab[] = ['conversation', 'timeline', 'raw', 'usage'];

  const labels: Record<ReviewTab, string> = {
    summary: 'Summary',
    diff: 'Findings & code',
    conversation: 'Conversation',
    timeline: 'Timeline',
    raw: 'Raw',
    usage: 'Usage',
  };
</script>

<svelte:head><title>{title} · kritika</title></svelte:head>

<main class="page review-page" bind:this={page}>
  <div class="page-inner">
    <StateView {res} retry={() => res.load()}>
      {#snippet children(d)}
        {@const r = d.review}
        {@const cancelable = isActive(r.status) && !r.cancelRequestedAt}
        <header class="page-head">
          <p class="crumbs">
            <a href={href({ name: 'pulls', slug })}>Pull requests</a> /
            <a class="mono" href={href(pullRoute(slug, r.pull))}>{r.pull.repository}#{r.pull.number}</a>
          </p>
          <h1>{r.pull.title}</h1>
          <p class="meta-line">
            <ReviewStatusTile status={r.status} />
            <span>started <Time iso={r.createdAt} /></span>
            {#if r.status === 'skipped' && r.skipReason}<span>skipped: {skipText[r.skipReason]}</span>{/if}
            {#if r.priorReviewId}
              <span>after <a href={href({ name: 'review', slug, id: r.priorReviewId })}>the review before</a></span>
            {/if}
          </p>
          <details class="review-details"><summary>Review details</summary><ReviewMeta {r} scopeReason={r.scopeReason} /></details>
          {#if r.newestReviewId}
            <p class="notice newer-notice" role="note">
              The pull request has been reviewed again since.
              <a href={href({ name: 'review', slug, id: r.newestReviewId })}>Open its latest review</a>
            </p>
          {/if}
          {#if r.error}<p class="error-text" role="note">{r.error}</p>{/if}
          {#if r.cancelRequestedAt}<p class="small muted">cancel requested <Time iso={r.cancelRequestedAt} /></p>{/if}
          {#if r.pullState !== 'open'}<NoMoreReviews merged={r.pullMerged} />{/if}
          {#if isAdmin()}
            <div class="page-actions">
              <ActionButton
                label="Re-run"
                title="Re-run the review?"
                body={rerunBody(`${r.pull.repository}#${r.pull.number}`, r.pullState === 'open', r.pullMerged)}
                path={rerunPath(slug, r.pull)}
                done="Re-run queued"
                ondone={() => res.load()}
              />
              {#if cancelable}
                <ActionButton
                  label="Cancel review"
                  title="Cancel this review?"
                  body="The run stops at its next checkpoint and nothing more is posted to the pull request."
                  path={cancelPath(slug, id)}
                  danger
                  done="Cancel requested"
                  ondone={() => res.load()}
                />
              {/if}
            </div>
          {/if}
        </header>

        {#if tab === 'diff' && d.summary}
          <section class="review-conclusion" aria-label="Review summary">
            <h2>{d.summary.headline || 'Review summary'}</h2>
            <Markdown text={d.summary.take} />
            {#if d.summary.impact?.length}<div class="merge-impact"><strong>Merge impact</strong><ReviewImpact notes={d.summary.impact} /></div>{/if}
          </section>
        {/if}

        <!-- What the review said comes first; how it ran sits apart, to the right. -->
        <nav class="tabs" aria-label="Review sections">
          {#each REVIEW_TABS as t (t)}
            {#if t === RUN_TABS[0]}<span class="tabs-group-label">Run</span>{/if}
            <a class="tab" class:active={t === tab} aria-current={t === tab ? 'page' : undefined} href={href({ name: 'review', slug, id, tab: t, finding: t === 'diff' ? selectedFinding || undefined : undefined })} {@attach t === tab && revealInNav}>
              <span class="tab-label" data-label={labels[t]}>{labels[t]}</span>{#if t === 'summary' && d.findings.length}<span class="tab-count">{d.findings.length}</span>{/if}
            </a>
          {/each}
        </nav>

        <section class="tab-panel" aria-label={labels[tab]}>
          {#each visited as view (view)}
            <div class="review-tab" hidden={view !== tab}>
              {#if view === 'summary'}
                <SummaryTab {slug} {d} oninspect={() => inspection++} finding={tab === view ? finding : undefined} />
              {:else if view === 'diff'}
                <DiffTab {base} {d} {version} {finding} {inspection} bind:selected={selectedFinding} active={tab === view} />
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
      {/snippet}
    </StateView>
  </div>
</main>
