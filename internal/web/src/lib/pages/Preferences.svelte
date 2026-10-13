<script lang="ts">
  import PageHeader from '../components/PageHeader.svelte';
  // The viewer's own settings: how the dashboard writes times for them, and
  // its theme.
  // Each is saved as it is chosen, and kept with the user, so it holds in
  // any browser they sign in from.
  import { session, saveSettings } from '../session.svelte';
  import { clock, stamp } from '../time.svelte';
  import { zoneKnown } from '../dates';
  import Segmented from '../components/Segmented.svelte';
  import TimeZonePicker from '../components/TimeZonePicker.svelte';
  import Spinner from '../Spinner.svelte';

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

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Your settings" description="Display preferences, saved to your account across browsers." />
    {#if settings}
      <section class="panel" aria-labelledby="pref-time">
        <header class="panel-head">
          <h2 id="pref-time">Dates and times</h2>
          <span class="small muted text-ui-sm">Now: {now}</span>
        </header>
        <dl class="deflist prefs grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-2.5 mx-3 text-ui-sm">
          <dt><label for="pref-zone">Time zone</label></dt>
          <dd>
            <TimeZonePicker value={settings.timeZone} {zones} onchange={(timeZone) => saveSettings({ timeZone })} />
            {#if settings.timeZone && !zoneKnown(settings.timeZone)}
              <span class="small muted text-ui-sm">This browser does not know {settings.timeZone}, and uses its own.</span>
            {/if}
            <p class="small muted text-ui-sm">Charts and tables by day count each day in UTC, whatever the zone.</p>
          </dd>
          <dt>Clock</dt>
          <dd><Segmented label="Clock" options={CLOCKS} value={settings.clock} onchange={(clock) => saveSettings({ clock })} /></dd>
        </dl>
      </section>
      <section class="panel" aria-labelledby="pref-look">
        <header class="panel-head"><h2 id="pref-look">Appearance</h2></header>
        <dl class="deflist prefs grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-2.5 mx-3 text-ui-sm">
          <dt>Theme</dt>
          <dd>
            <Segmented label="Theme" options={THEMES} value={settings.theme} onchange={(theme) => saveSettings({ theme })} />
            <p class="small muted text-ui-sm">Light or dark holds in every browser you sign in from. Left to the browser, each follows its own system.</p>
          </dd>
        </dl>
      </section>
    {:else}
      <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted" aria-live="polite"><Spinner size={16} /> Loading…</p>
    {/if}
  </div>
</main>
