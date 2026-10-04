<script lang="ts">
  import { onMount } from 'svelte';
  import { basePath } from './lib/base';
  import { router, initRouter, href, navigate, parse, replace } from './lib/router.svelte';
  import { getJSON, sendJSON, ApiError, signinState } from './lib/api.svelte';
  import { initEvents, closeEvents, stream } from './lib/events.svelte';
  import { theme, cycleTheme, initTheme } from './lib/theme.svelte';
  import { initClock } from './lib/time.svelte';
  import { timestamp } from './lib/dates';
  import { initKeyboard, help, toggleHelp, togglePalette } from './lib/keyboard.svelte';
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
    mdiAccountOutline,
    mdiLogout,
    mdiChevronDown,
    mdiUnfoldMoreHorizontal,
    mdiCheck,
  } from './lib/icons';
  import { INSTANCE_TABS, SECTIONS, SECTION_ORDER, pageIn, pageOfInstance, scopeOf, sectionOf, type Section } from './lib/sections';
  import type { Route } from './lib/routes';
  import { focusOnMount, revealInNav } from './lib/focus';
  import Icon from './lib/Icon.svelte';
  import Palette from './lib/Palette.svelte';
  import SignIn from './lib/SignIn.svelte';
  import Page from './lib/pages/Page.svelte';
  import Toasts from './lib/components/Toasts.svelte';
  import SetupBanner from './lib/pages/admin/SetupBanner.svelte';
  import { session, loadMeta } from './lib/session.svelte';
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

  async function loadMe(): Promise<void> {
    try {
      session.me = await getJSON<Me>('/api/v1/me');
      initEvents();
    } catch (err) {
      // A 401 already redirected to #/signin (see api.svelte.ts); anything
      // else leaves `me` unset and the shell renders signed-out.
      if (!(err instanceof ApiError && err.status === 401)) console.error('load me:', err);
    }
  }

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
      console.error('sign out:', err);
    }
    closeEvents();
    session.me = undefined;
    navigate({ name: 'signin' });
  }

  // The account the route is in the scope of; none in the instance's.
  const currentSlug = $derived(scopeOf(router.route));
  const currentSection = $derived(sectionOf(router.route));
  const instanceTabs = $derived(INSTANCE_TABS.filter((t) => (!t.admin || me?.admin) && (!t.several || (me?.accounts.length ?? 0) > 1)));

  const instanceIcon: Partial<Record<Route['name'], string>> = {
    overview: mdiViewGridOutline,
    instanceQueue: mdiTrayFull,
    console: mdiConsoleLine,
  };

  const sectionIcon: Record<Section, string> = {
    analytics: mdiChartBoxOutline,
    pulls: mdiSourcePull,
    rules: mdiScaleBalance,
    settings: mdiCogOutline,
  };

  const themeIconPath = $derived(
    theme.pref === 'auto' ? mdiThemeLightDark : theme.pref === 'dark' ? mdiWeatherNight : mdiWhiteBalanceSunny,
  );

  // Keep the help dialog's Tab from escaping to the page behind the backdrop;
  // Escape (global handler) and the backdrop close it.
  function trapTab(e: KeyboardEvent): void {
    if (e.key === 'Tab') e.preventDefault();
  }

  // The account and user menus are native <details>, which have no built-in
  // Escape handling and stay open on an outside click or once a link in
  // them is followed, so all three are wired up by hand here.
  let accountMenuEl = $state<HTMLDetailsElement | undefined>(undefined);
  let userMenuEl = $state<HTMLDetailsElement | undefined>(undefined);

  function closeMenus(): void {
    if (accountMenuEl) accountMenuEl.open = false;
    if (userMenuEl) userMenuEl.open = false;
  }

  function onMenuKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.stopPropagation();
      closeMenus();
    }
  }

  function onDocumentClick(e: MouseEvent): void {
    for (const el of [accountMenuEl, userMenuEl]) {
      if (el?.open && !el.contains(e.target as Node)) el.open = false;
    }
  }

  $effect(() => {
    void router.route;
    closeMenus();
  });
</script>

<svelte:window onclick={onDocumentClick} />

{#if router.route.name === 'signin'}
  <SignIn />
{:else}
  <div class="app">
    <header class="topbar">
      <div class="topbar-row">
        <a class="brand" href="#/">
          <img src="{basePath}/favicon.svg" width="22" height="22" alt="" />
          <span class="wordmark marked">kritika</span>
        </a>

        {#if me && me.accounts.length > 0}
          <!-- Escape from anywhere in the open menu closes it before the window's handlers see it. -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <details class="menu account-menu" bind:this={accountMenuEl} onkeydown={onMenuKeydown}>
            <summary class="account-button" title="Switch between the instance and an account">
              {#if currentSlug}
                <span class="mono">{currentSlug}</span>
              {:else}
                <span>Instance</span>
              {/if}
              <Icon path={mdiUnfoldMoreHorizontal} size={14} label="Switch scope" />
            </summary>
            <nav class="menu-panel account-panel" aria-label="Scope">
              <a
                class="menu-item"
                href={href(pageOfInstance(router.route, instanceTabs.some((t) => t.route.name === 'instanceQueue')))}
                aria-current={currentSlug ? undefined : 'true'}
              >
                <span class="menu-check">{#if !currentSlug}<Icon path={mdiCheck} size={14} />{/if}</span>
                Instance
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
          </details>
        {/if}

        <div class="spacer"></div>

        <div class="actions">
          {#if me}
            <span
              class="live"
              class:live-down={stream.down}
              aria-live="polite"
              title={stream.down ? `Live updates stopped at ${timestamp(stream.since)}; this page may be out of date.` : 'Live updates on'}
            >
              <span class="live-dot" aria-hidden="true"></span>
              {#if stream.down}Reconnecting…{:else}<span class="sr-only">Live updates on</span>{/if}
            </span>
          {/if}
          <button class="btn btn-icon" onclick={togglePalette} title="Go to (Ctrl/⌘ K)">
            <Icon path={mdiMagnify} label="Go to" />
          </button>
          <button class="btn btn-icon" onclick={toggleHelp} title="Keyboard shortcuts (?)">
            <Icon path={mdiKeyboardOutline} label="Keyboard shortcuts" />
          </button>
          <button class="btn btn-icon" onclick={cycleTheme} title={`Theme: ${theme.pref}`}>
            <Icon path={themeIconPath} label="Toggle theme" />
          </button>
          {#if me}
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <details class="menu user-menu" bind:this={userMenuEl} onkeydown={onMenuKeydown}>
              <summary class="btn btn-icon" title={me.user.displayName}>
                <Icon path={mdiAccountOutline} label="User" />
                <Icon path={mdiChevronDown} size={12} />
              </summary>
              <div class="menu-panel user-panel">
                <p class="user-name">{me.user.displayName}</p>
                <p class="user-email">{me.user.email}</p>
                <button class="btn" onclick={signOut}>
                  <Icon path={mdiLogout} size={14} /> Sign out
                </button>
                {#if session.meta?.version}
                  <p class="user-version mono" title="kritika {session.meta.version}">kritika {session.meta.version}</p>
                {/if}
              </div>
            </details>
          {/if}
        </div>
      </div>

      {#if me && !currentSlug}
        <nav class="sections" aria-label="Instance">
          {#each instanceTabs as t (t.route.name)}
            {@const on = router.route.name === t.route.name}
            <a class="section-tab" class:active={on} aria-current={on ? 'page' : undefined} href={href(t.route)} {@attach on && revealInNav}>
              <Icon path={instanceIcon[t.route.name] ?? mdiViewGridOutline} size={15} />
              <span class="section-label">{t.label}</span>
            </a>
          {/each}
        </nav>
      {:else if me && currentSlug}
        <nav class="sections" aria-label="Sections">
          {#each SECTION_ORDER as s (s)}
            <a
              class="section-tab"
              class:active={currentSection === s}
              aria-current={currentSection === s ? 'page' : undefined}
              href={href(SECTIONS[s].home(currentSlug))}
              {@attach currentSection === s && revealInNav}
            >
              <Icon path={sectionIcon[s]} size={15} />
              <span class="section-label">{SECTIONS[s].label}</span>
            </a>
          {/each}
        </nav>
      {/if}
    </header>

    <div class="main-col">
      {#if me?.admin}<SetupBanner />{/if}
      <Page route={router.route} />
    </div>
    <Palette {me} />
    <Toasts />
    {#if help.open}
      <div class="help-overlay">
        <button class="help-backdrop" aria-label="Close keyboard shortcuts" onclick={toggleHelp}></button>
        <div
          class="help-card"
          role="dialog"
          aria-modal="true"
          aria-label="Keyboard shortcuts"
          tabindex="-1"
          {@attach focusOnMount}
          onkeydown={trapTab}
        >
          <h2>Keyboard shortcuts</h2>
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
        </div>
      </div>
    {/if}
  </div>
{/if}
