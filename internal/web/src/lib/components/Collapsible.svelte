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
  <Collapsible.Trigger class="collapsible-head">
    <Icon path={open ? mdiChevronDown : mdiChevronRight} size={14} />
    <span class="collapsible-title">{title}</span>
    {#if meta}<span class="collapsible-meta">{@render meta()}</span>{/if}
  </Collapsible.Trigger>
  <!-- Bits UI keeps a closed body in the page, hidden; a body here may be a
       transcript or a file, so it is rendered only while open. -->
  <Collapsible.Content class="collapsible-body">{#if open}{@render children()}{/if}</Collapsible.Content>
</Collapsible.Root>
