<script lang="ts">
  // A modal dialog, on Bits UI's: it traps focus, locks the page behind it,
  // closes on Escape or a click outside, and renders its body only while
  // open, so an input inside (a password, say) never outlives it. Focus
  // returns to whatever opened it.
  import type { Snippet } from 'svelte';
  import { Dialog } from 'bits-ui';

  interface Props {
    open: boolean;
    title: string;
    children: Snippet;
    footer?: Snippet;
    onclose?: () => void;
    // Where focus goes on close when the opener is gone from the page (a
    // save that remounted it, or a disabled button that dropped focus to
    // the body): a selector, by default the page title.
    fallback?: string;
    // wide makes room for a form.
    wide?: boolean;
  }
  let { open = $bindable(false), title, children, footer, onclose, fallback = 'main h1', wide = false }: Props = $props();
  let restore: HTMLElement | null = null;

  // The dialog opens from state, not a trigger of its own, so what to
  // return focus to is noted here, before focus moves in.
  function opening(): void {
    restore = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  }

  function closing(e: Event): void {
    e.preventDefault();
    if (restore && restore !== document.body && restore.isConnected) {
      restore.focus();
    } else {
      const el = document.querySelector<HTMLElement>(fallback) ?? document.querySelector<HTMLElement>('main h1');
      if (el) {
        if (!el.hasAttribute('tabindex')) el.setAttribute('tabindex', '-1');
        el.focus();
      }
    }
    restore = null;
  }
</script>

<Dialog.Root bind:open onOpenChange={(now) => !now && onclose?.()}>
  <Dialog.Portal>
    <Dialog.Overlay class="overlay" />
    <Dialog.Content class={['modal modal-centered dialog', wide && 'dialog-wide']} onOpenAutoFocus={opening} onCloseAutoFocus={closing}>
      <Dialog.Title>
        {#snippet child({ props })}<h2 {...props} class="dialog-title">{title}</h2>{/snippet}
      </Dialog.Title>
      <div class="dialog-body">{@render children()}</div>
      {#if footer}<div class="dialog-actions">{@render footer()}</div>{/if}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
