<script lang="ts">
  // One headline number: its label, value, and change against the window
  // before, coloured by whether that change is good rather than by its sign.
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
  }: {
    label: string;
    value: string;
    sub?: string;
    // define says what the number counts.
    define?: string;
    now?: number | null;
    before?: number | null;
    // good is the direction that is good news; unset, a change is neither.
    good?: 'up' | 'down';
    // points compares two percentages by their difference.
    points?: boolean;
  } = $props();

  const delta = $derived(now === undefined || before === undefined ? undefined : change(now, before, points));
  const tone = $derived(!delta || delta.dir === 0 || !good ? 'muted' : (delta.dir > 0) === (good === 'up') ? 'ok' : 'danger');
</script>

{#snippet content()}
  <span class="stat-label text-ui-sm text-muted">{label}</span>
  <span class="stat-line flex flex-wrap items-baseline gap-2">
    <span class="stat-value text-[length:26px] font-bold tracking-[-0.01em]">{value}</span>
    {#if delta}<span class="delta tone-{tone}" title="Against the window before">{delta.text}</span>{/if}
  </span>
  {#if sub}<span class="stat-sub text-ui-sm text-muted">{sub}</span>{/if}
{/snippet}

<div class="stat min-w-0 border-b border-solid border-b-line">
  {#if define}
    <Tooltip text={define} class="flex flex-col gap-1 w-full pt-1 px-0 pb-4 border-0 bg-transparent bg-none text-left cursor-help" aria-label={`About ${label}: ${[value, delta?.text, sub].filter(Boolean).join(', ')}`}>
      {@render content()}
    </Tooltip>
  {:else}
    <div class="flex flex-col gap-1 pt-1 px-0 pb-4">{@render content()}</div>
  {/if}
</div>
