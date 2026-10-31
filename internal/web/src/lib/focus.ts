import type { Attachment } from 'svelte/attachments';

// revealInNav scrolls a navigation's current item into view, for a strip
// that scrolls sideways on a narrow screen.
export const revealInNav: Attachment<HTMLElement> = (node) => {
  const nav = node.closest('nav');
  if (!nav) return;
  const reveal = (): void => {
    const item = node.getBoundingClientRect();
    const strip = nav.getBoundingClientRect();
    if (item.left < strip.left) nav.scrollLeft += item.left - strip.left;
    else if (item.right > strip.right) nav.scrollLeft += item.right - strip.right;
  };
  reveal();
  const observer = new ResizeObserver(reveal);
  observer.observe(nav);
  observer.observe(node);
  return () => observer.disconnect();
};

// focusWhenShown focuses the element target selects once the page shows
// it, since a page loads its data first. It focuses the field inside the
// element, as a search lands on a setting. With section, it focuses the
// element itself, scrolled to the top, as a jump to a part of a page does.
// A choice drawn as a segmented control focuses its current option.
export function focusWhenShown(target: string, section = false): void {
  const deadline = performance.now() + 3000;
  const attempt = (): void => {
    const el = document.querySelector<HTMLElement>(target);
    if (!el) {
      if (performance.now() < deadline) requestAnimationFrame(attempt);
      return;
    }
    const field = section
      ? null
      : el.matches('input, select, textarea')
        ? el
        : el.querySelector<HTMLElement>('input, select, textarea, [role="radio"][aria-checked="true"]');
    const focus = field ?? el;
    if (!field && !focus.hasAttribute('tabindex')) focus.setAttribute('tabindex', '-1');
    focus.scrollIntoView({ block: section ? 'start' : 'center' });
    focus.focus({ preventScroll: true });
  };
  requestAnimationFrame(attempt);
}
