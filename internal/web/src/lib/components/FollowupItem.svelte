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
  import RowToggle from './RowToggle.svelte';
  import { rowToggles } from '../rowclick';

  let { slug, f, showPull = false }: { slug: string; f: Followup; showPull?: boolean } = $props();
  let open = $state(false);
  const id = $derived(`fu-${f.id}`);
  // The question is where it was asked, in a thread or on the conversation,
  // and so is its answer; the notice that a limit was reached always goes on
  // the conversation.
  const asked = $derived((f.inline ? threadUrl : commentUrl)(f.pullUrl, f.commentId));
  const answered = $derived((f.inline && f.status !== 'limited' ? threadUrl : commentUrl)(f.pullUrl, f.replyCommentId));
</script>

<!-- The chevron is the keyboard's toggle; the card's whitespace is the pointer's, as in the tables. -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<li class="followup row-toggles border-b border-solid border-b-line py-2 px-3 flex flex-col gap-1 cursor-pointer" onclick={(e) => { if (rowToggles(e) && !(e.target as Element).closest('.followup-transcript')) open = !open; }}>
  <div class="followup-head flex flex-wrap items-center gap-y-1.5 gap-x-2.5">
    <Pill tone={followupTone[f.status]} label={f.status} />
    <span><strong>{f.author}</strong> asked</span>
    {#if f.inline}<span class="mono small text-ui-sm">{f.path}:{f.line}</span>{:else}<span class="small muted text-ui-sm">on the conversation</span>{/if}
    {#if showPull}
      <a class="mono small text-ui-sm" href={href(pullRoute(slug, f))}>{f.repository}#{f.number}</a>
    {/if}
    {#if f.model}<span class="mono small muted text-ui-sm">{f.model}</span>{/if}
    <Time iso={f.createdAt} />
    <span class="spacer flex-auto"></span>
    <RowToggle bind:open label={`Transcript of ${f.author}'s question`} controls={id} />
  </div>
  {#if f.reason}<p class="small muted followup-reason text-ui-sm">{f.reason}</p>{/if}
  <p class="small followup-links text-ui-sm flex flex-wrap gap-y-1 gap-x-3.5 m-0">
    {#if asked}
      <a class="external" href={asked} target="_blank" rel="noopener noreferrer">The question on GitHub <Icon path={mdiOpenInNew} size={11} /></a>
    {/if}
    {#if answered}
      <a class="external" href={answered} target="_blank" rel="noopener noreferrer">kritika's reply <Icon path={mdiOpenInNew} size={11} /></a>
    {/if}
  </p>
  {#if open}
    <div {id} class="followup-transcript mt-1.5" role="region" aria-label={`Transcript of ${f.author}'s question`}><FollowupTranscript {slug} commentId={f.commentId} /></div>
  {/if}
</li>
