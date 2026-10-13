<script lang="ts">
  // One headline number: its label, value, and change against the window
  // before, coloured by whether that change is good rather than by its sign.
  // Several side by side go in a `.stats` group, which draws the border.
  import type { Snippet } from 'svelte';
  import { change } from '../format';
  import Tooltip from './Tooltip.svelte';

  let {
    label,
    value,
    sub = '',
    define = '',
    now,
    before,
    good,
    points = false,
    href,
    pending = false,
    children,
  }: {
    label: string;
    // value unset is a number still loading (pending) or one that could not be.
    value?: string;
    sub?: string;
    // define says what the number counts.
    define?: string;
    now?: number | null;
    before?: number | null;
    // good is the direction that is good news; unset, a change is neither.
    good?: 'up' | 'down';
    // points compares two percentages by their difference.
    points?: boolean;
    // href makes the whole tile the link to where the number comes from.
    href?: string;
    pending?: boolean;
    // children go under the number: a meter, a retry button.
    children?: Snippet;
  } = $props();

  const delta = $derived(now === undefined || before === undefined ? undefined : change(now, before, points));
  const tone = $derived(!delta || delta.dir === 0 || !good ? 'muted' : (delta.dir > 0) === (good === 'up') ? 'ok' : 'danger');
  const busy = $derived(value === undefined && pending);
</script>

{#snippet content()}
  <span class="stat-label text-ui-sm text-muted">{label}</span>
  {#if value !== undefined}
    <span class="stat-line flex flex-wrap items-baseline gap-2">
      <span class="stat-value text-[length:24px] font-bold tracking-[-0.01em] tabular-nums">{value}</span>
      {#if delta}<span class="delta tone-{tone}" title="Against the window before">{delta.text}</span>{/if}
    </span>
    {#if sub}<span class="stat-sub text-ui-sm text-muted">{sub}</span>{/if}
  {:else if pending}
    <span class="skeleton-value block bg-line rounded-[4px] w-14 h-9" aria-label={`Loading ${label.toLowerCase()}`}></span>
    <span class="skeleton-line block bg-line rounded-[4px] h-3 w-[70%]"></span>
  {:else}
    <span class="stat-sub text-ui-sm text-muted">Unavailable</span>
  {/if}
  {@render children?.()}
{/snippet}

{#if href}
  <a class="stat" {href} aria-busy={busy}>{@render content()}</a>
{:else if define && value !== undefined}
  <div class="stat">
    <Tooltip text={define} class="stat-hint flex flex-col gap-1 w-full p-0 border-0 bg-transparent bg-none text-left cursor-help" aria-label={`About ${label}: ${[value, delta?.text, sub].filter(Boolean).join(', ')}`}>
      {@render content()}
    </Tooltip>
  </div>
{:else}
  <div class="stat" aria-busy={busy}>{@render content()}</div>
{/if}
