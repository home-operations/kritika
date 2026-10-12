<script lang="ts">
  // A modal dialog, on Bits UI's: it traps focus, locks the page behind it,
  // closes on Escape (or a click outside for ordinary dialogs), and renders only while
  // open, so an input inside (a password, say) never outlives it. Focus
  // returns to whatever opened it.
  import { tick, type Snippet } from 'svelte';
  import { Dialog, AlertDialog } from 'bits-ui';

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
    // A moving selection can return to a different target than the opener.
    returnFocus?: string;
    // wide makes room for a form.
    wide?: boolean;
    side?: boolean;
    alert?: boolean;
  }
  let { open = $bindable(false), title, children, footer, onclose, fallback = 'main h1', returnFocus, wide = false, side = false, alert = false }: Props = $props();
  const Modal = $derived(alert ? AlertDialog : Dialog);
  let restore: HTMLElement | null = null;
  let content = $state<HTMLDivElement | null>(null);

  // The dialog opens from state, not a trigger of its own, so what to
  // return focus to is noted here, before focus moves in.
  function opening(e: Event): void {
    restore = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (alert) {
      e.preventDefault();
      void tick().then(() => content?.querySelector<HTMLElement>('[data-alert-dialog-cancel]')?.focus());
    }
  }

  function closing(e: Event): void {
    e.preventDefault();
    const target = returnFocus ? document.querySelector<HTMLElement>(returnFocus) : restore;
    if (target && target !== document.body && target.isConnected) {
      target.focus();
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

<Modal.Root bind:open onOpenChange={(now) => !now && onclose?.()}>
  <Modal.Portal>
    <Modal.Overlay class="overlay" />
    <Modal.Content bind:ref={content} class={['modal modal-centered dialog', wide && 'dialog-wide', side && 'dialog-side']} onOpenAutoFocus={opening} onCloseAutoFocus={closing}>
      <Modal.Title>
        {#snippet child({ props })}<h2 {...props} class="dialog-title">{title}</h2>{/snippet}
      </Modal.Title>
      {#if alert}
        <AlertDialog.Description>
          {#snippet child({ props })}<div {...props} class="dialog-body">{@render children()}</div>{/snippet}
        </AlertDialog.Description>
      {:else}
        <div class="dialog-body">{@render children()}</div>
      {/if}
      {#if footer}<div class="dialog-actions">{@render footer()}</div>{/if}
    </Modal.Content>
  </Modal.Portal>
</Modal.Root>
