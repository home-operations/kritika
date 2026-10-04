<script lang="ts">
  // One line of highlighted code: its tokens, each in its colour.
  import type { Token } from '../highlight';
  let { line }: { line: Token[] } = $props();
</script>

{#each line as t, i (i)}{#if t.color || t.italic}<span class:code-italic={t.italic} style:color={t.color}>{t.text}</span>{:else}{t.text}{/if}{/each}
