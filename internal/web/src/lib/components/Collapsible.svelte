<script lang="ts">
  // A disclosure, on Bits UI's collapsible: a real button with
  // aria-expanded controlling a region, so it is keyboard- and
  // screen-reader-operable.
  import type { Snippet } from 'svelte';
  import { Collapsible } from 'bits-ui';
  import Icon from '../Icon.svelte';
  import { mdiChevronDown, mdiChevronRight } from '../icons';

  interface Props {
    title: string;
    meta?: Snippet;
    open?: boolean;
    tone?: 'danger' | '';
    children: Snippet;
  }
  let { title, meta, open = $bindable(false), tone = '', children }: Props = $props();
</script>

<Collapsible.Root bind:open class={['collapsible', tone === 'danger' && 'tone-danger-edge']}>
  <!-- meta sits beside the button, not in it, so the button's name is the title alone. -->
  <div class="collapsible-row flex items-center gap-3 min-w-0">
    <Collapsible.Trigger class="collapsible-head flex-auto flex items-center gap-2 py-2.5 px-4 border-0 bg-transparent bg-none text-ink text-left cursor-pointer min-w-0">
      <Icon path={open ? mdiChevronDown : mdiChevronRight} size={14} />
      <span class="collapsible-title font-semibold text-ui-sm min-w-0 wrap-anywhere">{title}</span>
    </Collapsible.Trigger>
    {#if meta}<span class="collapsible-meta inline-flex items-center gap-1.5 min-w-0 overflow-hidden text-ellipsis whitespace-nowrap pr-4">{@render meta()}</span>{/if}
  </div>
  <!-- Bits UI keeps a closed body in the page, hidden; a body here may be a
       transcript or a file, so it is rendered only while open. -->
  <Collapsible.Content class="collapsible-body py-3 px-4 border-t border-solid border-t-line flex flex-col gap-2">{#if open}{@render children()}{/if}</Collapsible.Content>
</Collapsible.Root>
