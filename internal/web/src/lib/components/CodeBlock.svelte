<script lang="ts">
  // Preformatted text with a copy button. Anything past maxLines is clipped
  // behind a "show all" toggle so one huge tool result doesn't bury a page.
  // Code in a language highlight.ts knows is highlighted once that has
  // loaded, and shown plain until then.
  import { highlight, languageOf } from '../highlight';
  import { Highlighted } from '../tokens.svelte';
  import Copy from './Copy.svelte';
  import Tokens from './Tokens.svelte';

  interface Props {
    text: string;
    label?: string;
    maxLines?: number;
    copy?: boolean;
    // plain renders prose (sans, wrapped) instead of code.
    plain?: boolean;
    // lang is the code's language: a name, an alias or the path of its
    // file. Without one the label is tried, which is often a path.
    lang?: string;
  }
  let { text, label, maxLines = 40, copy = true, plain = false, lang }: Props = $props();
  let expanded = $state(false);

  const lines = $derived(text.split('\n'));
  const clipped = $derived(!expanded && lines.length > maxLines);
  const shown = $derived(clipped ? lines.slice(0, maxLines).join('\n') : text);

  const language = $derived(plain ? undefined : languageOf(lang ?? label));
  const tokens = new Highlighted(
    () => text,
    (code) => highlight(code, language),
    () => !!language,
  );
  const highlighted = $derived(language ? tokens.value : undefined);
</script>

<div class="code-block" class:plain>
  {#if label || copy}
    <div class="code-head">
      {#if label}<span class="code-label mono">{label}</span>{/if}
      <span class="spacer"></span>
      {#if copy}<Copy {text} />{/if}
    </div>
  {/if}
  {#if highlighted}
    <pre class="mono" data-lang={language}>{#each clipped ? highlighted.slice(0, maxLines) : highlighted as line, i (i)}{#if i}{'\n'}{/if}<Tokens {line} />{/each}</pre>
  {:else}
    <pre class:mono={!plain}>{shown}</pre>
  {/if}
  {#if lines.length > maxLines}
    <button class="btn btn-small code-more" aria-expanded={expanded} onclick={() => (expanded = !expanded)}>
      {expanded ? 'Show less' : `Show all ${lines.length} lines`}
    </button>
  {/if}
</div>
