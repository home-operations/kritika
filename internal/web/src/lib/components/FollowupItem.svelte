<script lang="ts">
  import type { Followup } from '../types';
  import { followupTone } from '../format';
  import { href } from '../router.svelte';
  import { pullRoute, commentUrl, threadUrl } from '../links';
  import Icon from '../Icon.svelte';
  import { mdiOpenInNew } from '../icons';
  import Pill from './Pill.svelte';
  import Time from './Time.svelte';
  import FollowupTranscript from './FollowupTranscript.svelte';

  let { slug, f, showPull = false }: { slug: string; f: Followup; showPull?: boolean } = $props();
  let open = $state(false);
  const id = $derived(`fu-${f.id}`);
  // The question is where it was asked, in a thread or on the conversation,
  // and so is its answer; the notice that a limit was reached always goes on
  // the conversation.
  const asked = $derived((f.inline ? threadUrl : commentUrl)(f.pullUrl, f.commentId));
  const answered = $derived((f.inline && f.status !== 'limited' ? threadUrl : commentUrl)(f.pullUrl, f.replyCommentId));
</script>

<li class="followup">
  <div class="followup-head">
    <Pill tone={followupTone[f.status]} label={f.status} />
    <span><strong>{f.author}</strong> asked</span>
    {#if f.inline}<span class="mono small">{f.path}:{f.line}</span>{:else}<span class="small muted">on the conversation</span>{/if}
    {#if showPull}
      <a class="mono small" href={href(pullRoute(slug, f))}>{f.repository}#{f.number}</a>
    {/if}
    {#if f.model}<span class="mono small muted">{f.model}</span>{/if}
    <Time iso={f.createdAt} />
    <span class="spacer"></span>
    <button class="btn btn-small" aria-expanded={open} aria-controls={open ? id : undefined} onclick={() => (open = !open)}>
      {open ? 'Hide transcript' : 'Transcript'}
    </button>
  </div>
  {#if f.reason}<p class="small muted followup-reason">{f.reason}</p>{/if}
  <p class="small followup-links">
    {#if asked}
      <a class="external" href={asked} target="_blank" rel="noopener noreferrer">The question on GitHub <Icon path={mdiOpenInNew} size={11} /></a>
    {/if}
    {#if answered}
      <a class="external" href={answered} target="_blank" rel="noopener noreferrer">kritika's reply <Icon path={mdiOpenInNew} size={11} /></a>
    {/if}
  </p>
  {#if open}
    <div {id} class="followup-transcript"><FollowupTranscript {slug} commentId={f.commentId} /></div>
  {/if}
</li>
