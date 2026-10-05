<script lang="ts">
  // The viewer's own settings: how the dashboard writes times for them, and
  // its theme.
  // Each is saved as it is chosen, and kept with the user, so it holds in
  // any browser they sign in from.
  import { session, saveSettings } from '../session.svelte';
  import { clock, stamp } from '../time.svelte';
  import { zoneKnown } from '../dates';
  import Segmented from '../components/Segmented.svelte';
  import Spinner from '../Spinner.svelte';

  const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const settings = $derived(session.me?.settings);
  // A zone chosen in another browser may be one this one does not list.
  const zones = $derived([...new Set([...(settings?.timeZone ? [settings.timeZone] : []), ...Intl.supportedValuesOf('timeZone')])].sort());

  const CLOCKS = [
    { value: '', label: 'Auto' },
    { value: '12', label: '12-hour' },
    { value: '24', label: '24-hour' },
  ] as const;

  const now = $derived(stamp(new Date(clock.now).toISOString()));

  const THEMES = [
    { value: '', label: 'Auto' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ] as const;
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
            <span class="select">
              <select id="pref-zone" value={settings.timeZone} onchange={(e) => saveSettings({ timeZone: e.currentTarget.value })}>
                <option value="">Auto</option>
                {#each zones as z (z)}<option value={z}>{z}</option>{/each}
              </select>
            </span>
            {#if settings.timeZone && !zoneKnown(settings.timeZone)}
              <span class="small muted">This browser does not know {settings.timeZone}, and uses its own.</span>
            {/if}
            <p class="small muted">Charts and tables by day count each day in UTC, whatever the zone.</p>
          </dd>
          <dt>Clock</dt>
          <dd><Segmented label="Clock" options={CLOCKS} value={settings.clock} onchange={(clock) => saveSettings({ clock })} /></dd>
        </dl>
      </section>
      <section class="panel" aria-labelledby="pref-look">
        <header class="panel-head"><h2 id="pref-look">Appearance</h2></header>
        <dl class="deflist prefs">
          <dt>Theme</dt>
          <dd>
            <Segmented label="Theme" options={THEMES} value={settings.theme} onchange={(theme) => saveSettings({ theme })} />
            <p class="small muted">Light or dark holds in every browser you sign in from. Left to the browser, each follows its own system.</p>
          </dd>
        </dl>
      </section>
    {:else}
      <p class="state-msg" aria-live="polite"><Spinner size={16} /> Loading…</p>
    {/if}
  </div>
</main>
