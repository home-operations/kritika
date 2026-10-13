<script lang="ts">
  import { href } from '../../router.svelte';
  import { Resource } from '../../resource.svelte';
  import type { Transcript } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import Conversation from '../../components/conversation/Conversation.svelte';

  let { active, base, version, slug }: { active: boolean; base: string; version: number; slug: string } = $props();
  const res = Resource.json<Transcript>(() => `${base}/transcript`);
  $effect(() => {
    void version;
    if (active) void res.load();
  });
</script>

<StateView {res} retry={() => res.load()} isEmpty={(t) => t.turns.length === 0} empty="No model calls recorded for this review.">
  {#snippet children(t)}
    <!-- A run that carried on another review's conversation recorded only what it added to it. -->
    {@const carried = t.turns.find((x) => x.carriedReviewId)?.carriedReviewId}
    {#if carried}
      <p class="notice carried-notice mb-2.5 border border-solid border-line bg-surface rounded-md py-2.5 px-4 text-ui-md" role="note">
        This review carried on the conversation of <a href={href({ name: 'review', slug, id: carried, tab: 'conversation' })}>the review before</a>;
        its earlier messages are there, not repeated here.
      </p>
    {/if}
    <Conversation transcript={t} />
  {/snippet}
</StateView>
