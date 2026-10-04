<script lang="ts">
  // The viewer's own settings: how the dashboard writes times for them.
  // Each is saved as it is chosen, and kept with the user, so it holds in
  // any browser they sign in from.
  import { sendJSON } from '../api.svelte';
  import { session } from '../session.svelte';
  import { describe } from '../manage';
  import { toast } from '../toast.svelte';
  import { applyDatePrefs, clock } from '../time.svelte';
  import { hour12Of, timestamp, zoneKnown } from '../dates';
  import type { UserSettings } from '../types';
  import Segmented from '../components/Segmented.svelte';

  const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const settings = $derived(session.me?.settings);
  // A zone chosen in another browser may be one this one does not list.
  const zones = $derived([...new Set([...(settings?.timeZone ? [settings.timeZone] : []), ...Intl.supportedValuesOf('timeZone')])].sort());

  const CLOCKS = [
    { value: '', label: `Browser's (${hour12Of() ? '12-hour' : '24-hour'})` },
    { value: '12', label: '12-hour' },
    { value: '24', label: '24-hour' },
  ] as const;

  const now = $derived((clock.rev, timestamp(new Date(clock.now).toISOString())));

  // save applies a choice at once and keeps it; one the server refuses is
  // taken back.
  async function save(change: Partial<UserSettings>): Promise<void> {
    const me = session.me;
    if (!me) return;
    const before = me.settings;
    me.settings = { ...before, ...change };
    applyDatePrefs(me.settings);
    try {
      await sendJSON('PUT', '/api/v1/me/settings', me.settings);
    } catch (err) {
      me.settings = before;
      applyDatePrefs(before);
      toast(`Not saved: ${describe(err)}`, 'danger');
    }
  }
</script>

<svelte:head><title>Your settings · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <header class="page-head">
      <h1>Your settings</h1>
      <p class="muted small">How the dashboard looks to you. They are kept with your user, so they hold in any browser you sign in from.</p>
    </header>
    {#if settings}
      <section class="panel" aria-labelledby="pref-time">
        <header class="panel-head">
          <h2 id="pref-time">Dates and times</h2>
          <span class="small muted">Now: {now}</span>
        </header>
        <dl class="deflist prefs">
          <dt><label for="pref-zone">Time zone</label></dt>
          <dd>
            <select id="pref-zone" value={settings.timeZone} onchange={(e) => save({ timeZone: e.currentTarget.value })}>
              <option value="">Browser's ({browserZone})</option>
              {#each zones as z (z)}<option value={z}>{z}</option>{/each}
            </select>
            {#if settings.timeZone && !zoneKnown(settings.timeZone)}
              <span class="small muted">This browser does not know {settings.timeZone}, and uses its own.</span>
            {/if}
            <p class="small muted">Charts and tables by day count each day in UTC, whatever the zone.</p>
          </dd>
          <dt>Clock</dt>
          <dd><Segmented label="Clock" options={CLOCKS} value={settings.clock} onchange={(clock) => save({ clock })} /></dd>
        </dl>
      </section>
    {:else}
      <p class="state-msg" aria-live="polite">Loading…</p>
    {/if}
  </div>
</main>
