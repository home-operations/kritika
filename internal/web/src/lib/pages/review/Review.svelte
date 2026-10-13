<script lang="ts">
  import { untrack } from 'svelte';
  import { getJSON } from '../../api.svelte';
  import { href, navigate } from '../../router.svelte';
  import type { ReviewTab, Route } from '../../routes';
  import { Resource, errorMessage, live, pollJobs } from '../../resource.svelte';
  import { isActive, jobCauseText, rerunBody, shortSha, skipText } from '../../format';
  import { pullRoute, repoRoute, rerunPath, cancelPath, accountApi } from '../../links';
  import { safeHref } from '../../markdown';
  import { isAdmin } from '../../session.svelte';
  import { clock, stamp } from '../../time.svelte';
  import { lifecycle } from '../../lifecycle';
  import type { PullDetail, Review, ReviewDetail } from '../../types';
  import ActionButton from '../../components/ActionButton.svelte';
  import PageHeader from '../../components/PageHeader.svelte';
  import StateView from '../../components/StateView.svelte';
  import ReviewStatusTile from '../../components/ReviewStatusTile.svelte';
  import Time from '../../components/Time.svelte';
  import Icon from '../../Icon.svelte';
  import NoMoreReviews from '../../components/NoMoreReviews.svelte';
  import FollowupItem from '../../components/FollowupItem.svelte';
  import ReviewBody from './ReviewBody.svelte';

  let { slug, id, owner, repo, number, run, tab = 'summary', finding }: {
    slug: string; id?: string; owner?: string; repo?: string; number?: number;
    run?: string; tab?: ReviewTab; finding?: string;
  } = $props();
  let page = $state<HTMLElement>();
  const account = $derived(accountApi(slug));
  const res: Resource<ReviewDetail> = new Resource(async () => {
    const detail = await getJSON<ReviewDetail>(base);
    if (!id && (detail.review.pull.repository.toLowerCase() !== `${owner}/${repo}`.toLowerCase() || detail.review.pull.number !== number)) {
      throw new Error('This review belongs to a different pull request. Select another run.');
    }
    return detail;
  }, () => base);
  const pull = new Resource(() => getJSON<PullDetail>(pullPath!), () => pullPath ?? '');
  const pullPath = $derived(owner && repo && number !== undefined
    ? `${account}/pulls/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/${number}`
    : res.data ? `${account}/pulls/${res.data.review.pull.repository.split('/').map(encodeURIComponent).join('/')}/${res.data.review.pull.number}` : undefined);
  const latest = $derived(pull.data?.reviews.find((r) => r.status !== 'skipped'));
  // Live history updates must not discard the reader's current diff and filters.
  let initialRun = $state<string>();
  const selectedId = $derived(id ?? run ?? initialRun);
  $effect(() => {
    if (!id && !run && latest) untrack(() => { initialRun ??= latest.id; });
  });
  const base = $derived(`${account}/reviews/${encodeURIComponent(selectedId ?? '')}`);
  let version = $state(0);
  let activityDirty = $state(false);

  $effect(() => { if (pullPath) void pull.load(); });
  $effect(() => { if (selectedId) void res.load(); });
  // Events lack a PR identifier; review lifecycle changes can introduce a new run.
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' || e.kind === 'followup'), (dirty) => {
    if (dirty.has('followup')) activityDirty = true;
    if (dirty.has('review') || dirty.has('resync')) {
      activityDirty = false;
      if (pullPath) void pull.load();
    }
  }));
  $effect(() => {
    if (tab === 'activity' && activityDirty && pullPath) {
      activityDirty = false;
      void pull.load();
    }
  });
  $effect(() => live((e) => e.account === slug && (e.kind === 'review' ? e.id : e.reviewId) === selectedId && !!res.data && isActive(res.data.review.status), () => {
    void res.load();
    version++;
  }));
  $effect(() => { if (pull.data?.job) return pollJobs(() => void pull.load()); });

  const r = $derived(res.data?.review);
  const p = $derived(pull.data?.pull);
  const ref = $derived(p ?? r?.pull);
  const historical = $derived(!!r && (!!r.newestReviewId || (!!latest && r.id !== latest.id && r.status !== 'skipped')));
  const pullState = $derived(p?.state ?? r?.pullState);
  const merged = $derived(p?.merged ?? r?.pullMerged ?? false);
  const job = $derived(pull.data?.job);
  const forgeUrl = $derived(safeHref(ref?.url ?? ''));
  const latestAttempt = $derived(pull.data?.reviews[0]);
  const openFindings = $derived(res.data?.findings.filter((f) => f.status === 'open').length ?? 0);
  const title = $derived(id && r ? `${r.status} · ${r.pull.repository}#${r.pull.number} review` : ref ? `${ref.title} · ${ref.repository}#${ref.number}` : 'Pull request');

  function to(nextTab: ReviewTab, nextFinding?: string): Route {
    if (id) return { name: 'review', slug, id, tab: nextTab, finding: nextFinding };
    return { name: 'pull', slug, owner: owner!, repo: repo!, number: number!, run: nextFinding ? selectedId : run, tab: nextTab, finding: nextFinding };
  }

  function selectRun(value: string): void {
    if (!ref) return;
    if (!value) initialRun = latest?.id;
    navigate({ ...pullRoute(slug, ref), run: value || undefined, tab });
  }

  function skippedReason(review: Review): string {
    return review.skipReason ? skipText[review.skipReason] : review.error || 'No review was produced.';
  }

  function openLatest(e: MouseEvent): void {
    if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    selectRun('');
  }

  function labelColor(color: string): string | undefined {
    return /^[0-9a-f]{3,8}$/i.test(color) ? `#${color}` : undefined;
  }

  function refresh(): void {
    if (pullPath) void pull.load();
    if (selectedId) void res.load();
  }
</script>

<svelte:head><title>{title} · kritika</title></svelte:head>

<main id="main-content" class="page review-page flex-auto min-w-0 min-h-0 overflow-auto relative" bind:this={page}>
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <div class="page-head pr-workspace-head flex flex-col gap-2 pb-4 border-b border-solid border-b-line">
      <PageHeader title={ref?.title ?? (owner && repo ? `${owner}/${repo}#${number}` : 'Pull request')} {slug} parents={[{ label: 'Pull requests', route: { name: 'pulls', slug } }, ...(ref ? [{ label: ref.repository, route: repoRoute(slug, ref.repository) }] : [])]} />
      {#if ref}<p class="mono small muted text-ui-sm"><a href={href(pullRoute(slug, ref))} onclick={openLatest}>{ref.repository}#{ref.number}</a></p>{/if}
      {#if p}
        {@const life = lifecycle(p)}
        <p class="meta-line flex flex-wrap items-center gap-y-1 gap-x-3 text-muted text-ui-sm">
          <span class="lifecycle-badge tone-{life.tone}"><Icon path={life.icon} size={13} /> {life.label}</span>
          <span><strong>{p.author}</strong> wants to merge <span class="mono">{p.headRef}</span> into <span class="mono">{p.baseRef}</span></span>
          <span>current head <span class="mono" title={p.headSha}>{shortSha(p.headSha)}</span>, last synchronized <Time iso={p.updatedAt} /></span>
          {#each p.labels as label (label.name)}<span class="label-chip text-ui-xs py-0 px-[7px] rounded-[999px] border border-solid border-[color-mix(in_srgb,_var(--label,_var(--border))_60%,_var(--border))] text-ink" style:--label={labelColor(label.color)}>{label.name}</span>{/each}
        </p>
      {/if}
      <div class="workspace-actions flex items-center flex-wrap gap-2.5">
        {#if ref}
          <label class="run-picker flex items-center flex-wrap gap-2.5 text-ui-sm">Review run
            <select aria-label="Review run" value={historical || id ? selectedId : run ?? ''} onchange={(e) => selectRun(e.currentTarget.value)}>
              <option value="">Latest review{latest ? ` · ${latest.status}` : ''}</option>
              {#if r && !pull.data?.reviews.some((review) => review.id === r.id)}<option value={r.id}>{r.status} · {stamp(r.createdAt)}</option>{/if}
              {#each pull.data?.reviews ?? [] as review (review.id)}
                <option value={review.id}>{review.status} · {shortSha(review.headSha)} · {stamp(review.createdAt)}</option>
              {/each}
            </select>
          </label>
          {#if forgeUrl}<a class="btn btn-primary" href={forgeUrl} target="_blank" rel="noopener noreferrer">Review on GitHub ↗</a>{/if}
          {#if isAdmin()}
            <ActionButton label="Re-run" title="Re-run the review?" body={rerunBody(`${ref.repository}#${ref.number}`, pullState === 'open', merged)} path={rerunPath(slug, ref)} done="Re-run queued" ondone={refresh} />
            {#if r && isActive(r.status) && !r.cancelRequestedAt}
              <ActionButton label="Cancel review" title="Cancel this review?" body="The run stops at its next checkpoint and nothing more is posted to the pull request." path={cancelPath(slug, r.id)} danger done="Cancel requested" ondone={refresh} />
            {/if}
          {/if}
        {/if}
      </div>
      {#if r}
        <div class="review-signals grid gap-2 py-3 px-0">
          <p class="meta-line flex flex-wrap items-center gap-y-1 gap-x-3 text-muted text-ui-sm"><strong>Execution</strong><ReviewStatusTile status={r.status} /><span>started <Time iso={r.createdAt} /></span><span>reviewed commit <span class="mono" title={r.headSha}>{shortSha(r.headSha)}</span></span></p>
          <p class="meta-line flex flex-wrap items-center gap-y-1 gap-x-3 text-muted text-ui-sm" aria-label="Review outcome"><strong>Findings</strong>
            {#if r.status === 'completed'}<span>{openFindings} open · {res.data!.findings.length} reported</span>
            {:else if isActive(r.status)}<span>Review in progress. Findings are not final.</span>
            {:else if r.status === 'skipped'}<span>No review performed. {skippedReason(r)}</span>
            {:else}<span>Review incomplete. {openFindings} open {openFindings === 1 ? 'finding' : 'findings'} recorded; this is not a clean review.</span>{/if}
          </p>
        </div>
        {#if historical && ref}
          <p class="notice newer-notice border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">Historical review. A newer review is available. <a href={href(pullRoute(slug, ref))} onclick={openLatest}>Open its latest review</a></p>
        {:else if p && r.headSha !== p.headSha}
          <p class="notice border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">The current head has not been reviewed by this run.</p>
        {/if}
        {#if r.priorReviewId}<p class="small muted text-ui-sm">Continues <a href={href({ name: 'review', slug, id: r.priorReviewId })}>the review before</a>.</p>{/if}
        {#if r.error && r.status !== 'skipped'}<p class="notice error-text text-danger whitespace-pre-wrap [word-break:break-word] m-0 border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">Execution problem: {r.error} <a href={href(to('timeline'))}>Inspect run details</a></p>{/if}
        {#if r.cancelRequestedAt}<p class="small muted text-ui-sm">cancel requested <Time iso={r.cancelRequestedAt} /></p>{/if}
      {/if}
      {#if pullState === 'closed'}<NoMoreReviews {merged} />{/if}
      {#if p?.paused && p.state === 'open'}
        <p class="notice paused-notice border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">Automatic reviews of this pull request are paused: a push is recorded, not reviewed. A comment asking the bot to <span class="mono">review</span> still reviews it, and one asking it to <span class="mono">resume</span> turns them back on.</p>
      {/if}
      {#if r && latest && latestAttempt?.status === 'skipped' && r.id === latest.id && r.id !== latestAttempt.id}
        <p class="notice border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">Latest attempt skipped: {skippedReason(latestAttempt)} Showing the last review.</p>
      {/if}
      {#if job && (job.state !== 'running' || job.lastError || historical)}
        <p class="notice job-notice m-0 border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">
          {#if job.state === 'running'}A review is running, attempt {job.attempt} of {job.maxAttempts}.
          {:else if job.lastError}A review is waiting to run again: attempt {job.attempt} of {job.maxAttempts} failed, the next {Date.parse(job.scheduledAt) > clock.now ? 'is' : 'was'} due <Time iso={job.scheduledAt} />.
          {:else}A review is queued.{/if}
          <!-- eslint-disable-next-line svelte/no-useless-mustaches -- Preserve the separator at the conditional boundary. -->
          {#if job.lastError}<span class="error-text text-danger text-ui-sm whitespace-pre-wrap [word-break:break-word] m-0">{#if job.cause}<strong>{jobCauseText[job.cause]}</strong>{' '}{/if}{job.lastError}</span>{/if}
          <a href={href({ name: 'queue', slug })}>Open the queue</a>
        </p>
      {/if}
    </div>

    {#if pull.error}
      <div class="state-msg state-error flex items-center gap-2 m-0 p-3 text-danger border border-solid border-[color-mix(in_srgb,_var(--danger)_35%,_var(--border))] [background:color-mix(in_srgb,_var(--danger)_6%,_transparent)] rounded-md" role="alert"><span>Pull request details: {errorMessage(pull.error)}</span><button class="btn" onclick={() => void pull.load()}>Retry pull request</button></div>
    {/if}
    {#if selectedId}
      <StateView {res} retry={() => res.load()}>
        {#snippet children(d)}
          {#key d.review.id}<ReviewBody {slug} {d} pull={pull.data} {base} {version} {tab} {finding} {page} {to} />{/key}
        {/snippet}
      </StateView>
    {:else if pull.data}
      <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">Not reviewed yet.</p>
      {#if latestAttempt?.status === 'skipped'}<p class="notice border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md">Latest attempt skipped: {skippedReason(latestAttempt)}</p>{/if}
      {#if pull.data.followups.length}<section class="panel" aria-label="Follow-ups"><header class="panel-head"><h2>Follow-ups</h2></header><ul class="followups list-none m-0 p-0">{#each pull.data.followups as f (f.id)}<FollowupItem {slug} {f} />{/each}</ul></section>{/if}
    {:else if !pull.error}<p class="state-msg flex items-center gap-2 m-0 p-3 text-muted" role="status">Loading pull request…</p>{/if}
  </div>
</main>
