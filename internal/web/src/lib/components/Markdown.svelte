<script lang="ts">
  // Renders the tree markdown.ts makes of model-written prose. Every value
  // goes through text interpolation (auto-escaped); links are http(s) only.
  import { parseMarkdown, type Block, type Inline } from '../markdown';
  import CodeBlock from './CodeBlock.svelte';
  let { text }: { text: string } = $props();
  const parsed = $derived(parseMarkdown(text));
</script>

{#snippet spans(list: Inline[])}
  {#each list as t, i (i)}{#if t.kind === 'text'}{t.text}{:else if t.kind === 'code'}<code>{t.text}</code>{:else if t.kind === 'break'}<br />{:else if t.kind === 'bold'}<strong>{@render spans(t.inlines)}</strong>{:else if t.kind === 'italic'}<em>{@render spans(t.inlines)}</em>{:else if t.kind === 'strike'}<del>{@render spans(t.inlines)}</del>{:else}<a href={t.href} target="_blank" rel="noopener noreferrer">{@render spans(t.inlines)}</a>{/if}{/each}
{/snippet}

{#snippet body(list: Block[])}
  {#each list as b, i (i)}
    {#if b.kind === 'code'}
      <CodeBlock text={b.text} label={b.lang || undefined} />
    {:else if b.kind === 'para'}
      <p>{@render spans(b.inlines)}</p>
    {:else if b.kind === 'heading'}
      <p class="md-heading md-h{Math.min(b.depth, 3)}">{@render spans(b.inlines)}</p>
    {:else if b.kind === 'rule'}
      <hr />
    {:else if b.kind === 'quote'}
      <blockquote>{@render body(b.blocks)}</blockquote>
    {:else if b.kind === 'list'}
      <svelte:element this={b.ordered ? 'ol' : 'ul'} start={b.ordered && b.start !== 1 ? b.start : undefined}>
        {#each b.items as item, j (j)}
          <li class:md-task={item.checked !== undefined}>
            {#if item.checked !== undefined}<input type="checkbox" checked={item.checked} disabled aria-label={item.checked ? 'done' : 'to do'} />{/if}
            {@render body(item.blocks)}
          </li>
        {/each}
      </svelte:element>
    {:else}
      <div class="table-wrap">
        <table class="md-table">
          <thead>
            <tr>{#each b.header as cell, j (j)}<th scope="col" style:text-align={b.align[j] ?? undefined}>{@render spans(cell)}</th>{/each}</tr>
          </thead>
          <tbody>
            {#each b.rows as row, r (r)}
              <tr>{#each row as cell, j (j)}<td style:text-align={b.align[j] ?? undefined}>{@render spans(cell)}</td>{/each}</tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/each}
{/snippet}

<div class="md">{@render body(parsed)}</div>
