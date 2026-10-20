<script lang="ts">
  import { Resource } from '../resource.svelte';
  import { accountApi, pullRoute } from '../links';
  import { href } from '../router.svelte';
  import { isActive, skipText } from '../format';
  import type { Pull, ReviewDetail } from '../types';
  import StateView from './StateView.svelte';
  import Markdown from './Markdown.svelte';
  import Pill from './Pill.svelte';

  let { slug, pull }: { slug: string; pull: Pull } = $props();
  const res = Resource.live<ReviewDetail>(() => `${accountApi(slug)}/reviews/${encodeURIComponent(pull.lastReview!.id)}`);
  $effect(() => { void res.load(); });
</script>

<StateView {res} layout="inline" retry={() => res.load()}>
  {#snippet children(d)}
    <div class="row-preview-grid">
      <div class="min-w-0">
        <h2 class="preview-heading">Latest review</h2>
        {#if isActive(d.review.status)}<p class="muted">Review in progress. Findings are not final.</p>
        {:else if d.review.status === 'skipped'}<p class="muted">No review performed. {d.review.skipReason ? skipText[d.review.skipReason] : d.review.error || 'This run was skipped.'}</p>
        {:else if d.review.status !== 'completed'}<p class="text-danger">Review incomplete. Findings may be partial.</p>{/if}
        {#if d.review.headSha !== pull.headSha}<p class="muted">The current head has not been reviewed by this run.</p>{/if}
        {#if d.summary}<div class="preview-summary"><Markdown text={d.summary.headline || d.summary.take} /></div>
        {:else if d.review.status === 'completed'}<p class="muted">No summary available.</p>{/if}
        {#if d.review.error && d.review.status !== 'skipped'}<p class="text-danger line-clamp-2 wrap-anywhere">{d.review.error}</p>{/if}
        {#if d.review.confidence}<p class="small muted text-ui-sm mt-1">Confidence {d.review.confidence.score}/5 · {d.review.confidence.risk} risk{d.review.confidence.passed ? '' : ' · below threshold'}</p>{/if}
        <p class="small muted text-ui-sm mt-1"><span class="mono">{pull.headRef}</span> → <span class="mono">{pull.baseRef}</span></p>
      </div>
      <div class="min-w-0">
        <h2 class="preview-heading">Findings <span class="muted">({d.findings.length})</span></h2>
        {#if d.findings.length}
          <ul class="preview-findings">
            {#each d.findings.slice(0, 3) as finding (finding.id)}
              <li><span class="sev sev-{finding.severity}">{finding.severity.toUpperCase()}</span><a href={href({ ...pullRoute(slug, pull), run: d.review.id, tab: 'diff', finding: finding.id })}>{finding.title}</a>{#if finding.status !== 'open'}<Pill label={finding.status} />{/if}</li>
            {/each}
          </ul>
          {#if d.findings.length > 3}<p class="small muted text-ui-sm mt-1">Showing 3 of {d.findings.length} findings.</p>{/if}
        {:else}<p class="muted">{d.review.status === 'completed' ? 'No findings.' : 'No findings recorded yet.'}</p>{/if}
      </div>
    </div>
    <div class="preview-actions"><a class="btn btn-small" href={href({ ...pullRoute(slug, pull), run: d.review.id })}>Open full review →</a></div>
  {/snippet}
</StateView>
