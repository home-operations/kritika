<script lang="ts">
  import { getJSON } from '../../api.svelte';
  import { href, navigate } from '../../router.svelte';
  import type { ReviewTab, Route } from '../../routes';
  import { Resource, errorMessage, live, pollJobs } from '../../resource.svelte';
  import { isActive, jobCauseText, rerunBody, shortSha, skipText } from '../../format';
  import { pullRoute, rerunPath, cancelPath, accountApi } from '../../links';
  import { safeHref } from '../../markdown';
  import { isAdmin } from '../../session.svelte';
  import { clock, stamp } from '../../time.svelte';
  import { lifecycle } from '../../lifecycle';
  import type { PullDetail, ReviewDetail } from '../../types';
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
    if (!id && (detail.review.pull.repository !== `${owner}/${repo}` || detail.review.pull.number !== number)) {
      throw new Error('This review belongs to a different pull request. Select another run.');
    }
    return detail;
  }, () => base);
  const pull = new Resource(() => getJSON<PullDetail>(pullPath!), () => pullPath ?? '');
  const pullPath = $derived(owner && repo && number !== undefined
    ? `${account}/pulls/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/${number}`
    : res.data ? `${account}/pulls/${res.data.review.pull.repository.split('/').map(encodeURIComponent).join('/')}/${res.data.review.pull.number}` : undefined);
  const latest = $derived(pull.data?.reviews.find((r) => r.status !== 'skipped'));
  const selectedId = $derived(id ?? run ?? latest?.id);
  const base = $derived(`${account}/reviews/${encodeURIComponent(selectedId ?? '')}`);
  let version = $state(0);

  $effect(() => { if (pullPath) void pull.load(); });
  $effect(() => { if (selectedId) void res.load(); });
  $effect(() => live((e) => e.account === slug && e.kind !== 'index_run' && e.kind !== 'model_call', () => {
    if (pullPath) void pull.load();
  }));
  $effect(() => live((e) => e.account === slug && e.reviewId === selectedId && !!res.data && isActive(res.data.review.status), () => {
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
  const openFindings = $derived(res.data?.findings.filter((f) => f.status === 'open').length ?? 0);
  const title = $derived(id && r ? `${r.status} · ${r.pull.repository}#${r.pull.number} review` : ref ? `${ref.title} · ${ref.repository}#${ref.number}` : 'Pull request');

  function to(nextTab: ReviewTab, nextFinding?: string): Route {
    if (id) return { name: 'review', slug, id, tab: nextTab, finding: nextFinding };
    return { name: 'pull', slug, owner: owner!, repo: repo!, number: number!, run, tab: nextTab, finding: nextFinding };
  }

  function selectRun(value: string): void {
    if (!ref) return;
    navigate({ ...pullRoute(slug, ref), run: value || undefined, tab });
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

<main class="page review-page" bind:this={page}>
  <div class="page-inner">
    <div class="page-head pr-workspace-head">
      <PageHeader title={ref?.title ?? (owner && repo ? `${owner}/${repo}#${number}` : 'Pull request')} {slug} parents={[{ label: 'Pull requests', route: { name: 'pulls', slug } }]} />
      {#if ref}<p class="mono small muted">{ref.repository}#{ref.number}</p>{/if}
      {#if p}
        {@const life = lifecycle(p)}
        <p class="meta-line">
          <span class="lifecycle-badge tone-{life.tone}"><Icon path={life.icon} size={13} /> {life.label}</span>
          <span><strong>{p.author}</strong> wants to merge <span class="mono">{p.headRef}</span> into <span class="mono">{p.baseRef}</span></span>
          <span>current head <span class="mono" title={p.headSha}>{shortSha(p.headSha)}</span>, last synchronized <Time iso={p.updatedAt} /></span>
          {#each p.labels as label (label.name)}<span class="label-chip" style:--label={labelColor(label.color)}>{label.name}</span>{/each}
        </p>
      {/if}
      <div class="workspace-actions">
        {#if ref}
          <label class="run-picker">Review run
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
        <div class="review-signals">
          <p class="meta-line"><strong>Execution</strong><ReviewStatusTile status={r.status} /><span>started <Time iso={r.createdAt} /></span><span>reviewed commit <span class="mono" title={r.headSha}>{shortSha(r.headSha)}</span></span></p>
          <p class="meta-line" aria-label="Review outcome"><strong>Findings</strong>
            {#if r.status === 'completed'}<span>{openFindings} open · {res.data!.findings.length} reported</span>
            {:else if isActive(r.status)}<span>Review in progress. Findings are not final.</span>
            {:else}<span>Review incomplete. {openFindings} open findings recorded; this is not a clean review.</span>{/if}
          </p>
        </div>
        {#if historical && ref}
          <p class="notice newer-notice" role="note">Historical review. The pull request has been reviewed again since. <a href={href(pullRoute(slug, ref))}>Open its latest review</a></p>
        {:else if p && r.headSha !== p.headSha}
          <p class="notice" role="note">The current head has not been reviewed by this run.</p>
        {/if}
        {#if r.status === 'skipped'}<p class="notice">Review skipped{r.skipReason ? `: ${skipText[r.skipReason]}` : ''}.</p>{/if}
        {#if r.error}<p class="notice error-text" role="note">Execution problem: {r.error} <a href={href(to('timeline'))}>Inspect run details</a></p>{/if}
        {#if r.cancelRequestedAt}<p class="small muted">cancel requested <Time iso={r.cancelRequestedAt} /></p>{/if}
      {/if}
      {#if pullState === 'closed'}<NoMoreReviews {merged} />{/if}
      {#if p?.paused && p.state === 'open'}
        <p class="notice paused-notice" role="note">Automatic reviews of this pull request are paused: a push is recorded, not reviewed. A comment asking the bot to <span class="mono">review</span> still reviews it, and one asking it to <span class="mono">resume</span> turns them back on.</p>
      {/if}
      {#if !historical && pull.data?.reviews[0]?.status === 'skipped' && latest}
        {@const skipped = pull.data.reviews[0]}
        <p class="notice" role="note">Latest attempt skipped: {skipped.skipReason ? skipText[skipped.skipReason] : skipped.error || 'No review was produced.'} Showing the last review.</p>
      {/if}
      {#if job && (job.state !== 'running' || job.lastError || historical)}
        <p class="notice job-notice" role="note">
          {#if job.state === 'running'}A review is running, attempt {job.attempt} of {job.maxAttempts}.
          {:else if job.lastError}A review is waiting to run again: attempt {job.attempt} of {job.maxAttempts} failed, the next {Date.parse(job.scheduledAt) > clock.now ? 'is' : 'was'} due <Time iso={job.scheduledAt} />.
          {:else}A review is queued.{/if}
          {#if job.lastError}<span class="error-text">{#if job.cause}<strong>{jobCauseText[job.cause]}</strong>{' '}{/if}{job.lastError}</span>{/if}
          <a href={href({ name: 'queue', slug })}>Open the queue</a>
        </p>
      {/if}
    </div>

    {#if pull.error}
      <div class="state-msg state-error" role="alert"><span>Pull request details: {errorMessage(pull.error)}</span><button class="btn" onclick={() => void pull.load()}>Retry pull request</button></div>
    {/if}
    {#if selectedId}
      <StateView {res} retry={() => res.load()}>
        {#snippet children(d)}
          {#key d.review.id}<ReviewBody {slug} {d} pull={pull.data} {base} {version} {tab} {finding} {page} {to} />{/key}
        {/snippet}
      </StateView>
    {:else if pull.data}
      <p class="state-msg">Not reviewed yet.</p>
      {#if pull.data.reviews[0]?.status === 'skipped'}<p class="notice">Latest attempt skipped: {pull.data.reviews[0].skipReason ? skipText[pull.data.reviews[0].skipReason] : pull.data.reviews[0].error || 'No review was produced.'}</p>{/if}
      {#if pull.data.followups.length}<section class="panel" aria-label="Follow-ups"><header class="panel-head"><h2>Follow-ups</h2></header><ul class="followups">{#each pull.data.followups as f (f.id)}<FollowupItem {slug} {f} />{/each}</ul></section>{/if}
    {:else if !pull.error}<p class="state-msg" role="status">Loading pull request…</p>{/if}
  </div>
</main>
