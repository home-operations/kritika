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

// A full settings replacement must commit before the next one starts.
// Keep the last acknowledged state for rollback if several writes fail.
let settingsWriter: {
  me: Me;
  saved: UserSettings;
  theme: typeof theme.pref;
  tail: Promise<void>;
} | undefined;

export function saveSettings(change: Partial<UserSettings>): Promise<void> {
  const me = session.me;
  if (!me) return Promise.resolve();
  const writer = settingsWriter?.me === me ? settingsWriter : (settingsWriter = {
    me, saved: me.settings, theme: theme.pref, tail: Promise.resolve(),
  });
  me.settings = { ...me.settings, ...change };
  const settings = me.settings;
  applySettings(settings);
  if (change.theme === '') setTheme('auto');
  const chosenTheme = theme.pref;
  writer.tail = writer.tail.then(async () => {
    if (session.me !== me) return;
    try {
      await sendJSON('PUT', '/api/v1/me/settings', settings);
      writer.saved = settings;
      writer.theme = chosenTheme;
    } catch (err) {
      if (session.me !== me) return;
      if (me.settings === settings) {
        me.settings = writer.saved;
        applySettings(writer.saved);
        setTheme(writer.theme);
      }
      toast(`Not saved: ${describe(err)}`, 'danger');
    }
  });
  return writer.tail;
}
