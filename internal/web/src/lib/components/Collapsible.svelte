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
  <Collapsible.Trigger class="collapsible-head w-full flex items-center gap-2 py-1.5 px-2.5 border-0 bg-transparent bg-none text-ink text-left cursor-pointer min-w-0">
    <Icon path={open ? mdiChevronDown : mdiChevronRight} size={14} />
    <span class="collapsible-title font-semibold text-ui-sm whitespace-nowrap">{title}</span>
    {#if meta}<span class="collapsible-meta inline-flex items-center gap-1.5 min-w-0 overflow-hidden text-ellipsis whitespace-nowrap">{@render meta()}</span>{/if}
  </Collapsible.Trigger>
  <!-- Bits UI keeps a closed body in the page, hidden; a body here may be a
       transcript or a file, so it is rendered only while open. -->
  <Collapsible.Content class="collapsible-body py-2 px-2.5 border-t border-solid border-t-line flex flex-col gap-2">{#if open}{@render children()}{/if}</Collapsible.Content>
</Collapsible.Root>
