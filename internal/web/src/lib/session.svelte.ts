// Who is signed in and what the server allows, shared by every page that
// shows or hides a control on it. App.svelte loads both; pages only read.
import { getJSON, sendJSON } from './api.svelte';
import { describe } from './manage';
import { toast } from './toast.svelte';
import { applyDatePrefs } from './time.svelte';
import { setTheme, theme } from './theme.svelte';
import type { Me, Meta, UserSettings } from './types';

export const session = $state<{ me: Me | undefined; meta: Meta | undefined }>({ me: undefined, meta: undefined });

// loadMeta fetches /api/v1/meta once; it needs no session. A failure leaves
// meta unset, which reads as management off.
export async function loadMeta(): Promise<void> {
  if (session.meta) return;
  try {
    session.meta = await getJSON<Meta>('/api/v1/meta');
  } catch (err) {
    console.error('load meta:', err);
  }
}

// hookURL is a webhook path under the dashboard's URL, which the webhook
// listener shares.
export function hookURL(path: string): string {
  return (session.meta?.webUrl ?? '').replace(/\/+$/, '') + path;
}

// isAdmin is whether the viewer administers the instance, and with it every
// account.
export function isAdmin(): boolean {
  return session.me?.admin === true;
}

// applySettings has the dashboard look as the user's settings say. A theme
// they left to the browser stays this browser's own.
export function applySettings(s: UserSettings): void {
  applyDatePrefs(s);
  if (s.theme) setTheme(s.theme);
}

// saveSettings applies a change to the viewer's settings at once and keeps
// it with their user; one the server refuses is taken back. A theme handed
// back to the browser has this one follow its system again.
export async function saveSettings(change: Partial<UserSettings>): Promise<void> {
  const me = session.me;
  if (!me) return;
  const before = { settings: me.settings, theme: theme.pref };
  me.settings = { ...me.settings, ...change };
  applySettings(me.settings);
  if (change.theme === '') setTheme('auto');
  try {
    await sendJSON('PUT', '/api/v1/me/settings', me.settings);
  } catch (err) {
    me.settings = before.settings;
    applySettings(before.settings);
    setTheme(before.theme);
    toast(`Not saved: ${describe(err)}`, 'danger');
  }
}
