<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { Dialog, Popover } from 'bits-ui';
  import { basePath } from './lib/base';
  import { router, initRouter, href, navigate, parse, replace } from './lib/router.svelte';
  import { getJSON, sendJSON, ApiError, signinState } from './lib/api.svelte';
  import { initEvents, closeEvents, stream } from './lib/events.svelte';
  import { theme, nextTheme, setTheme, initTheme } from './lib/theme.svelte';
  import { initClock, stamp } from './lib/time.svelte';
  import { initKeyboard, help, palette, closeOverlays, toggleHelp, togglePalette } from './lib/keyboard.svelte';
  import {
    mdiThemeLightDark,
    mdiWeatherNight,
    mdiWhiteBalanceSunny,
    mdiKeyboardOutline,
    mdiMagnify,
    mdiChartBoxOutline,
    mdiViewGridOutline,
    mdiConsoleLine,
    mdiTrayFull,
    mdiSourcePull,
    mdiScaleBalance,
    mdiCogOutline,
    mdiSourceRepository,
    mdiCurrencyUsd,
    mdiBugOutline,
    mdiClipboardTextClockOutline,
    mdiAccountOutline,
    mdiLogout,
    mdiChevronDown,
    mdiUnfoldMoreHorizontal,
    mdiCheck,
  } from './lib/icons';
  import { INSTANCE_TABS, SECTIONS, NAV_GROUPS, reportingRoute, pageIn, pageOfInstance, scopeOf, sectionOf, type Section } from './lib/sections';
  import type { Route } from './lib/routes';
  import { focusWhenShown, revealInNav } from './lib/focus';
  import Icon from './lib/Icon.svelte';
  import Palette from './lib/Palette.svelte';
  import SignIn from './lib/SignIn.svelte';
  import Page from './lib/pages/Page.svelte';
  import SettingsNav from './lib/pages/SettingsNav.svelte';
  import Toasts from './lib/components/Toasts.svelte';
  import SetupBanner from './lib/pages/admin/SetupBanner.svelte';
  import { describe } from './lib/manage';
  import { toast } from './lib/toast.svelte';
  import { session, loadMeta, applySettings, saveSettings } from './lib/session.svelte';
  import type { Me } from './lib/types';

  const me = $derived(session.me);

  onMount(() => {
    initTheme();
    initRouter();
    initKeyboard();
    initClock();
    void loadMeta();
    void loadMe();
  });

  let checkingSession = $state(true);
  let sessionError = $state('');

  async function loadMe(): Promise<void> {
    checkingSession = true;
    sessionError = '';
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(new Error('Session request timed out; try again.')), 15_000);
    try {
      session.me = await getJSON<Me>('/api/v1/me', controller.signal);
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 401)) sessionError = describe(controller.signal.aborted ? controller.signal.reason : err);
    } finally {
      clearTimeout(timeout);
      checkingSession = false;
    }
  }

  $effect(() => {
    if (!me) return;
    untrack(() => { applySettings(me.settings); initEvents(); });
    return closeEvents;
  });

  // A signed-in user never sits on the sign-in card, however they got there
  // (page load or in-app navigation): send them where a 401 bounced them
  // from, or the overview.
  $effect(() => {
    if (!me || router.route.name !== 'signin') return;
    const back = parse(signinState.returnTo || '#/');
    signinState.returnTo = '';
    replace(back.name === 'signin' ? { name: 'overview' } : back);
  });

  async function signOut(): Promise<void> {
    try {
      await sendJSON('POST', '/auth/logout');
    } catch (err) {
      toast(`Sign out failed: ${describe(err)}`, 'danger');
      return;
    }
    closeEvents();
    session.me = undefined;
    navigate({ name: 'signin' });
  }

  // The account the route is in the scope of; none in the instance's.
  const currentSlug = $derived(scopeOf(router.route));
  const currentSection = $derived(sectionOf(router.route));
  const instanceTabs = $derived(INSTANCE_TABS.filter((t) => (!t.admin || me?.admin)));

  const instanceIcon: Partial<Record<Route['name'], string>> = {
    overview: mdiViewGridOutline,
    instanceQueue: mdiTrayFull,
    instancePulls: mdiSourcePull,
    console: mdiConsoleLine,
  };

  const sectionIcon: Record<Section, string> = {
    overview: mdiViewGridOutline,
    analytics: mdiChartBoxOutline,
    pulls: mdiSourcePull,
    usage: mdiCurrencyUsd,
    repos: mdiSourceRepository,
    queue: mdiTrayFull,
    followups: mdiClipboardTextClockOutline,
    findings: mdiBugOutline,
    rules: mdiScaleBalance,
    settings: mdiCogOutline,
  };

  const themeIconPath = $derived(
    theme.pref === 'auto' ? mdiThemeLightDark : theme.pref === 'dark' ? mdiWeatherNight : mdiWhiteBalanceSunny,
  );

  // The theme button steps to the next theme, and keeps a light or dark
  // one with the user; auto is this browser's own to follow its system.
  function cycleTheme(): void {
    const next = nextTheme();
    if (session.me) void saveSettings({ theme: next === 'auto' ? '' : next });
    else setTheme(next);
  }

  // The scope and user menus are popovers: Bits UI closes one on Escape or
  // a click outside, and following a link in one closes it here.
  let scopeOpen = $state(false);
  let userOpen = $state(false);
  let navOpen = $state(false);

  $effect(() => {
    if (help.open || palette.open) navOpen = false;
  });

  $effect(() => {
    void router.route;
    navOpen = false;
    scopeOpen = false;
    userOpen = false;
  });
</script>

{#snippet workspaceNav()}
  {#if me && !currentSlug}
    <nav class="sections" aria-label="Instance">
      {#each instanceTabs as t (t.route.name)}
        {@const on = router.route.name === t.route.name}
        <a class="section-tab" class:active={on} aria-current={on ? 'page' : undefined} href={href(t.route)} {@attach on && revealInNav}>
          <Icon path={instanceIcon[t.route.name] ?? mdiViewGridOutline} size={15} />
          <span class="section-label" data-label={t.label}>{t.label}</span>
        </a>
      {/each}
    </nav>
  {:else if me && currentSlug}
    <nav class="sections" aria-label="Sections">
      {#each NAV_GROUPS as group (group.label)}
        <p class="workspace-heading">{group.label}</p>
        {#each group.sections.filter((s) => !SECTIONS[s].admin || me?.admin) as s (s)}
          {@const on = currentSection === s}
          <a class="section-tab" class:active={on} aria-current={on ? 'page' : undefined}
            href={href(reportingRoute(SECTIONS[s].home(currentSlug), router.route))}>
            <Icon path={sectionIcon[s]} size={15} />
            <span class="section-label" data-label={SECTIONS[s].label}>{SECTIONS[s].label}</span>
          </a>
        {/each}
      {/each}
    </nav>
  {/if}
  {#if me && router.route.name === 'console'}<SettingsNav />{/if}
{/snippet}

{#if checkingSession || (me && router.route.name === 'signin') || (!me && sessionError && router.route.name !== 'signin')}
  <main class="session-screen" aria-busy={checkingSession}>
    <img src="{basePath}/favicon.svg" width="40" height="40" alt="" />
    <span class="wordmark marked">kritika</span>
    {#if sessionError}
      <h1>Unable to load your session</h1>
      <p role="alert">{sessionError}</p>
      <button class="btn btn-primary" onclick={() => void loadMe()}>Retry</button>
    {:else}<p role="status">Loading your workspace…</p>{/if}
  </main>
{:else if !me}
  <SignIn />
{:else}
  <div class="app">
    <header class="topbar">
      <a class="skip-link" href="#main-content" onclick={(e) => { e.preventDefault(); focusWhenShown('main', true); }}>Skip to content</a>
      <div class="topbar-row">
        <a class="brand" href="#/">
          <img src="{basePath}/favicon.svg" width="22" height="22" alt="" />
          <span class="wordmark marked">kritika</span>
        </a>

        {#if me && me.accounts.length > 0}
          <Popover.Root bind:open={scopeOpen}>
            <Popover.Trigger class="account-button" title="Switch between the instance and an account">
              {#if currentSlug}
                <span class="mono">{currentSlug}</span>
              {:else}
                <span>All accounts</span>
              {/if}
              <Icon path={mdiUnfoldMoreHorizontal} size={14} label="Switch scope" />
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content class="menu-panel account-panel" align="start" sideOffset={6}>
                <nav aria-label="Scope">
              <a
                class="menu-item"
                href={href(pageOfInstance(router.route))}
                aria-current={currentSlug ? undefined : 'true'}
              >
                <span class="menu-check">{#if !currentSlug}<Icon path={mdiCheck} size={14} />{/if}</span>
                All accounts
              </a>
              <p class="menu-heading">Accounts</p>
              {#each me.accounts as slug (slug)}
                {@const on = slug === currentSlug}
                <a class="menu-item mono" href={href(pageIn(router.route, slug))} aria-current={on ? 'true' : undefined}>
                  <span class="menu-check">{#if on}<Icon path={mdiCheck} size={14} />{/if}</span>
                  {slug}
                </a>
              {/each}
                </nav>
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
        {/if}

        <div class="desktop-navigation">{@render workspaceNav()}</div>
        <div class="actions">
          <button class="btn btn-icon" onclick={togglePalette} title="Go to (Ctrl/⌘ K)">
            <Icon path={mdiMagnify} label="Go to" /><span class="search-label">Go to…</span><kbd class="search-label">⌘ K</kbd>
          </button>
          <button class="btn btn-icon keys-button" onclick={toggleHelp} title="Keyboard shortcuts (?)">
            <Icon path={mdiKeyboardOutline} label="Keyboard shortcuts" />
          </button>
          <button class="btn btn-icon" onclick={cycleTheme} title={`Theme: ${theme.pref}`}>
            <Icon path={themeIconPath} label="Toggle theme" />
          </button>
          {#if me}
            <Popover.Root bind:open={userOpen}>
              <Popover.Trigger class="btn btn-icon user-button" title={me.user.displayName}>
                <Icon path={mdiAccountOutline} label="User" />
                <Icon path={mdiChevronDown} size={12} />
              </Popover.Trigger>
              <Popover.Portal>
                <Popover.Content class="menu-panel user-panel" align="end" sideOffset={6}>
                <p class="user-name">{me.user.displayName}</p>
                <p class="user-email">{me.user.email}</p>
                <a class="btn" href={href({ name: 'preferences' })}><Icon path={mdiCogOutline} size={14} /> Settings</a>
                <button class="btn" onclick={signOut}>
                  <Icon path={mdiLogout} size={14} /> Sign out
                </button>
                {#if session.meta?.version}
                  <p class="user-version mono" title="kritika {session.meta.version}">kritika {session.meta.version}</p>
                {/if}
                </Popover.Content>
              </Popover.Portal>
            </Popover.Root>
          {/if}
        </div>
      </div>

      {#if me}
        <div class="mobile-navigation">
          <Dialog.Root bind:open={navOpen}>
            <Dialog.Trigger class="btn mobile-nav-trigger" aria-label="Open navigation">
              <Icon path={mdiViewGridOutline} size={16} />
              Navigation <span class="muted">· {currentSection ? SECTIONS[currentSection].label : 'All accounts'}</span>
              <Icon path={mdiChevronDown} size={14} />
            </Dialog.Trigger>
            <Dialog.Portal>
              <Dialog.Overlay class="overlay" />
              <Dialog.Content class="modal mobile-nav-panel" onclick={(e) => { if ((e.target as Element).closest('a')) navOpen = false; }}>
                <div class="mobile-nav-head">
                  <Dialog.Title>Navigation</Dialog.Title>
                  <Dialog.Close class="btn">Close</Dialog.Close>
                </div>
                <Dialog.Description class="small muted">{currentSlug || 'All accounts'}</Dialog.Description>
                {@render workspaceNav()}
              </Dialog.Content>
            </Dialog.Portal>
          </Dialog.Root>
        </div>
      {/if}
      {#if me}
        <span
          class="live"
          class:live-down={stream.down}
          aria-live="polite"
          title={stream.down ? `Live updates stopped at ${stamp(stream.since)}; this page may be out of date.` : 'Live updates on'}
        >
          <span class="live-dot" aria-hidden="true"></span>
          {#if stream.down}Reconnecting…{:else}<span class="sr-only">Live updates on</span>{/if}
        </span>
      {/if}
    </header>

    <div class="main-col" id="main-content" tabindex="-1">
      {#if me?.admin}<SetupBanner />{/if}
      <Page route={router.route} />
    </div>
    <Palette {me} />
    <Toasts />
    <Dialog.Root open={help.open} onOpenChange={(now) => !now && closeOverlays()}>
      <Dialog.Portal>
        <Dialog.Overlay class="overlay" />
        <Dialog.Content class="modal modal-centered help-card">
          <Dialog.Title>
            {#snippet child({ props })}<h2 {...props}>Keyboard shortcuts</h2>{/snippet}
          </Dialog.Title>
          <dl class="help-keys">
            <dt><kbd>Ctrl</kbd>/<kbd>⌘</kbd> <kbd>k</kbd></dt>
            <dd>go to a page</dd>
            <dt><kbd>?</kbd></dt>
            <dd>toggle this help</dd>
            <dt><kbd>j</kbd> / <kbd>k</kbd></dt>
            <dd>move down or up a list of pull requests or findings</dd>
            <dt><kbd>⏎</kbd></dt>
            <dd>open the row the cursor is on</dd>
            <dt><kbd>/</kbd></dt>
            <dd>search the list</dd>
            <dt><kbd>space</kbd></dt>
            <dd>select a pull request for a bulk action, as an admin</dd>
          </dl>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </div>
{/if}
