// rowToggles says whether a click on a table row must toggle its
// expanded detail: anywhere on the row that is not a control of its own
// (the title link, a checkbox, the chevron) and not a text selection.
export function rowToggles(e: MouseEvent): boolean {
  const target = e.target as Element;
  return !target.closest('a, button, input, label, select, summary') && !getSelection()?.toString();
}
