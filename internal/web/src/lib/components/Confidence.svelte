<script lang="ts">
  // The scorer's verdict on a review as one line, as the forge comment
  // carries it: the score against the threshold, the risk, and the reason,
  // which is prose with its GitHub references already linked.
  import type { Confidence } from '../types';
  import Markdown from './Markdown.svelte';
  let { c }: { c: Confidence } = $props();
</script>

<div class="confidence-line" title="scored by {c.model}">
  <strong>Confidence {c.score}/5</strong>{#if !c.passed}, below the {c.threshold} this repository asks for{/if}{#if c.risk}&nbsp;·
    {c.risk} risk{/if}{#if c.reason}: <Markdown text={c.reason} />{/if}
</div>
