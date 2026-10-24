// The instance administration's sections, which the command palette
// lists. Rune-free so tests can import it.
import type { ConsolePage } from './routes';

export interface SettingEntry {
  label: string;
  // page is the administration page that shows it, its accounts when unset.
  page?: ConsolePage;
  // target selects the element to focus once its page shows it.
  target: string;
  keywords: string;
}

// The administration's sections, in page order.
export const CONSOLE_SECTIONS: readonly SettingEntry[] = [
  { label: 'Setup', target: '#op-setup', keywords: 'checklist missing error refused reload' },
  { label: 'Accounts', target: '#op-accounts', keywords: 'served usage' },
  { label: 'Connections', page: 'connections', target: '#op-connections', keywords: 'github app forgejo gitlab gitea installations uninstall connections' },
  { label: 'Users', page: 'users', target: '#op-users', keywords: 'people members sign-ins sign out sessions' },
  { label: 'API tokens', page: 'users', target: '#op-tokens', keywords: 'personal access tokens revoke mcp agents' },
  { label: 'Download cache', page: 'settings', target: '#op-download-cache', keywords: 'gateway s3 storage bucket retention downloads' },
  { label: 'Most used tools', page: 'settings', target: '#op-tools', keywords: 'commands executions usage graph mise' },
  { label: 'Instance configuration', page: 'settings', target: '#op-instance', keywords: 'environment sources instance providers embedding' },
  { label: 'Admin audit log', page: 'audit', target: '#op-audit', keywords: 'history' },
];
