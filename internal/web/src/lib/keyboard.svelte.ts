// Keyboard-first navigation. Active only outside text inputs and without
// modifier keys, so it never fights the browser or a page's filter box.

// Two overlays: the shortcuts help ('?' or the topbar button) and the command
// palette (Cmd/Ctrl+K). They are mutually exclusive. Each is a Bits UI
// dialog, which closes on Escape and returns focus to what had it.
export const help = $state({ open: false });
export const palette = $state({ open: false });

export function closeOverlays(): void {
  help.open = false;
  palette.open = false;
}

export function toggleHelp(): void {
  const now = !help.open;
  closeOverlays();
  help.open = now;
}

export function togglePalette(): void {
  const now = !palette.open;
  closeOverlays();
  palette.open = now;
}

export function isTyping(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable);
}

export function initKeyboard(): void {
  window.addEventListener('keydown', (e) => {
    // Cmd/Ctrl+K first: it must work everywhere, including inside a text input.
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      togglePalette();
      e.preventDefault();
      return;
    }

    if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) return;

    // '?' toggles the help on any screen.
    if (e.key === '?') {
      toggleHelp();
      e.preventDefault();
    }
  });
}
