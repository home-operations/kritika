// The admin Configuration page's sections, which the command palette and
// the settings navigation both list. Rune-free so tests can import it.

export interface SettingEntry {
  label: string;
  // target selects the element to focus once its page shows it.
  target: string;
  keywords: string;
}

// The Configuration page's sections, in page order.
export const CONSOLE_SECTIONS: readonly SettingEntry[] = [
  { label: 'Setup', target: '#op-setup', keywords: 'checklist missing error refused reload' },
  { label: 'Accounts', target: '#op-accounts', keywords: 'served usage' },
  { label: 'Connections', target: '#op-connections', keywords: 'github app forgejo installations uninstall connections' },
  { label: 'Download cache', target: '#op-download-cache', keywords: 'gateway s3 storage bucket retention downloads' },
  { label: 'Most used tools', target: '#op-tools', keywords: 'commands executions usage graph mise' },
  { label: 'Instance configuration', target: '#op-instance', keywords: 'environment sources instance providers embedding' },
  { label: 'Admin audit log', target: '#op-audit', keywords: 'history' },
];
