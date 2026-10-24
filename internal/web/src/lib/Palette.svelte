<script lang="ts">
  // The Cmd/Ctrl+K command palette: jump to any page from anywhere, from a
  // registry of routes (global pages, plus the current account's pages when
  // the active route is inside one). A search also finds the sections of
  // the administration's pages, which the palette focuses once their page
  // shows them.
  import type { Route } from './router.svelte';
  import { INSTANCE_TABS, reportingRoute } from './sections';
  import { router, navigate, href } from './router.svelte';
  import { Command, Dialog } from 'bits-ui';
  import { palette, closeOverlays } from './keyboard.svelte';
  import Icon from './Icon.svelte';
  import type { Me, Page, Pull } from './types';
  import { apiQuery } from './query';
  import { createQueries } from '@tanstack/svelte-query';
  import { pullRoute, accountApi } from './links';
  import { CONSOLE_SECTIONS } from './settingsindex';
  import { focusWhenShown } from './focus';
  import {
    mdiMagnify,
    mdiLogin,
    mdiConsoleLine,
    mdiSourceRepository,
    mdiSourcePull,
    mdiTrayFull,
    mdiCurrencyUsd,
    mdiClipboardTextClockOutline,
    mdiCogOutline,
    mdiViewGridOutline,
    mdiBugOutline,
    mdiChartBoxOutline,
    mdiScaleBalance,
  } from './icons';

  let { me }: { me: Me | undefined } = $props();

  interface Entry {
    label: string;
    hint?: string;
    // words says the hint is a word, not a name: it is set in the text face.
    words?: boolean;
    route: Route;
    icon: string;
    // target selects the element to focus once the route shows it.
    target?: string;
    // keywords are matched as well as the label and hint.
    keywords?: string;
  }

  // currentSlug reads the account slug off whatever route is active, when the
  // route carries one — every account-scoped Route variant does.
  function currentSlug(r: Route): string | undefined {
    return 'slug' in r ? r.slug : undefined;
  }

  // Gated the same way as the sidebar: the administration's pages
  // and each account's audit log are for admins, and "Sign in" only makes
  // sense when there's no session yet.
  function buildEntries(r: Route, searching: boolean): Entry[] {
    const entries: Entry[] = [{ label: 'Overview', hint: 'instance', words: true, route: { name: 'overview' }, icon: mdiViewGridOutline, keywords: 'all accounts' }];
    if (me) {
      entries.push({ label: 'Pull requests', hint: 'all accounts', words: true, route: { name: 'instancePulls' }, icon: mdiSourcePull });
      entries.push({ label: 'Queue', hint: 'instance', words: true, route: { name: 'instanceQueue' }, icon: mdiTrayFull, keywords: 'jobs slots all accounts' });
      entries.push({ label: 'Analytics', hint: 'all accounts', words: true, route: { name: 'instanceAnalytics' }, icon: mdiChartBoxOutline, keywords: 'reviews dashboard' });
      entries.push({ label: 'Usage & costs', hint: 'all accounts', words: true, route: { name: 'instanceUsage' }, icon: mdiCurrencyUsd, keywords: 'usage cost tokens spend' });
    }
    if (me?.admin) {
      for (const t of INSTANCE_TABS.filter((t) => t.admin)) {
        entries.push({ label: t.label, hint: 'administration', words: true, route: t.route, icon: mdiConsoleLine, keywords: 'admin console instance settings configuration' });
      }
      if (searching) {
        for (const { label, page, target, keywords } of CONSOLE_SECTIONS) {
          entries.push({ label, hint: 'configuration', words: true, route: page ? { name: 'console', page } : { name: 'console' }, icon: mdiCogOutline, target, keywords });
        }
      }
    }
    if (me) {
      entries.push({ label: 'Your settings', route: { name: 'preferences' }, icon: mdiCogOutline, keywords: 'preferences time zone clock' });
    }
    if (!me) {
      entries.push({ label: 'Sign in', route: { name: 'signin' }, icon: mdiLogin });
    }
    // Every account the user can see gets its pages, current account first,
    // so any page of any account is a few keystrokes away.
    const current = currentSlug(r);
    const slugs = [...new Set([...(current ? [current] : []), ...(me?.accounts ?? [])])];
    for (const slug of slugs) {
      entries.push(
        { label: 'Overview', hint: slug, route: { name: 'account', slug }, icon: mdiViewGridOutline },
        { label: 'Analytics', hint: slug, route: reportingRoute({ name: 'analytics', slug }, r), icon: mdiChartBoxOutline, keywords: 'reviews dashboard' },
        { label: 'Repositories', hint: slug, route: { name: 'repos', slug }, icon: mdiSourceRepository },
        { label: 'Pull requests', hint: slug, route: { name: 'pulls', slug }, icon: mdiSourcePull },
        { label: 'Findings', hint: slug, route: { name: 'findings', slug }, icon: mdiBugOutline, keywords: 'bugs caught addressed' },
        { label: 'Rules', hint: slug, route: { name: 'rules', slug }, icon: mdiScaleBalance, keywords: 'rules context kritika.yaml' },
        { label: 'Queue', hint: slug, route: { name: 'queue', slug }, icon: mdiTrayFull },
        { label: 'Usage & costs', hint: slug, route: reportingRoute({ name: 'usage', slug }, r), icon: mdiCurrencyUsd, keywords: 'usage cost tokens spend' },
        { label: 'Follow-ups', hint: slug, route: { name: 'followups', slug }, icon: mdiClipboardTextClockOutline },
      );
      if (me?.admin) {
        entries.push({ label: 'Audit log', hint: slug, route: { name: 'admin', slug, section: 'audit' }, icon: mdiClipboardTextClockOutline, keywords: 'history' });
      }
    }
    for (const { slug, pull: p } of recent) {
      entries.push({ label: p.title, hint: `${p.repository}#${p.number}`, route: pullRoute(slug, p), icon: mdiSourcePull });
    }
    return entries;
  }

  const current = $derived(currentSlug(router.route));
  const slugs = $derived(me ? [...new Set([...(current ? [current] : []), ...me.accounts])] : []);
  const recentQueries = createQueries(() => ({
    queries: slugs.map((slug) => {
      const path = `${accountApi(slug)}/pulls?state=all&limit=20`;
      return {
        ...apiQuery<Page<Pull>>(path),
        enabled: palette.open,
      };
    }),
  }));
  const recent = $derived(recentQueries.flatMap((query, i) =>
    (query.data?.items ?? []).map((pull) => ({ slug: slugs[i], pull })),
  ).sort((a, b) => Number(b.slug === current) - Number(a.slug === current) || Date.parse(b.pull.updatedAt) - Date.parse(a.pull.updatedAt)));

  const loggedErrors = new WeakSet<Error>();
  $effect(() => {
    for (const query of recentQueries) {
      if (query.error && !loggedErrors.has(query.error)) {
        loggedErrors.add(query.error);
        console.error('palette recent pulls:', query.error);
      }
    }
  });

  let q = $state('');

  // Fresh state on every open: the component stays mounted between opens, so
  // drop the previous query.
  $effect(() => {
    if (palette.open) q = '';
  });

  const rows = $derived.by(() => {
    const needle = q.trim().toLowerCase();
    const entries = buildEntries(router.route, needle !== '');
    if (!needle) return entries;
    return entries.filter((e) => [e.label, e.hint, e.keywords].some((s) => s?.toLowerCase().includes(needle)));
  });

  // rowKey tells rows apart: a page and a section on it share a label and a
  // route, and differ by the section's target.
  const rowKey = (row: Entry) => `${row.label}\u0000${href(row.route)}\u0000${row.target ?? ''}`;

  function commit(row: Entry | undefined): void {
    if (!row) return;
    closeOverlays();
    navigate(row.route);
    if (row.target) focusWhenShown(row.target);
  }
</script>

<Dialog.Root open={palette.open} onOpenChange={(now) => !now && closeOverlays()}>
  <Dialog.Portal>
    <Dialog.Overlay class="overlay fixed [inset:0] z-[var(--z-overlay)] [background:color-mix(in_srgb,_var(--bg)_60%,_transparent)]" />
    <Dialog.Content class="modal palette top-[12vh] left-0 right-0 my-0 mx-auto w-[min(640px,_92vw)] max-h-[70vh] flex flex-col rounded-[10px] shadow-[0_16px_48px_color-mix(in_srgb,_var(--text)_25%,_transparent)] overflow-hidden" aria-label="Go to">
      <!-- The rows are already the ones that match, so Bits UI's own filter is off; it keeps the cursor and the arrow keys. -->
      <Command.Root class="palette-command flex flex-col min-h-0" shouldFilter={false} loop label="Go to">
        <div class="palette-input flex items-center gap-2.5 py-[11px] px-3.5 border-b border-solid border-b-line text-muted">
          <Icon path={mdiMagnify} size={16} />
          <Command.Input bind:value={q} placeholder="Go to…" aria-label="Go to" aria-controls="palette-results" />
          <span class="palette-esc flex-none"><kbd>Esc</kbd></span>
        </div>

        <Command.List id="palette-results" class="palette-body overflow-y-auto pt-1 px-0 pb-1.5" tabindex={0}>
          {#each rows as row (rowKey(row))}
            <Command.Item class="palette-row flex items-center gap-2.5 w-full py-[7px] px-3.5 border-0 bg-transparent text-inherit text-left cursor-pointer" value={rowKey(row)} onSelect={() => commit(row)}>
              <Icon path={row.icon} size={14} />
              <span class="row-main flex-auto min-w-0 flex flex-col">
                <span class="row-title font-medium overflow-hidden text-ellipsis whitespace-nowrap">{row.label}</span>
                {#if row.hint}<span class="row-sub inline-flex items-center gap-1 text-muted text-ui-sm" class:mono={!row.words}>{row.hint}</span>{/if}
              </span>
            </Command.Item>
          {/each}
          {#if q.trim() && !rows.length}
            <p class="palette-empty m-0 py-4 px-3.5 text-muted">Nothing matches “{q}”.</p>
          {/if}
        </Command.List>
      </Command.Root>

      <div class="palette-footer flex items-center gap-3.5 py-[7px] px-3.5 border-t border-solid border-t-line text-muted text-ui-xs">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>⏎</kbd> open</span>
      </div>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
