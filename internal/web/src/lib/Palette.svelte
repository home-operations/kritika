<script lang="ts">
  // The Cmd/Ctrl+K command palette: jump to any page from anywhere, from a
  // registry of routes (global pages, plus the current account's pages when
  // the active route is inside one). A search also finds the sections of
  // the admin Configuration page, which the palette focuses once its page
  // shows them.
  import type { Route } from './router.svelte';
  import { router, navigate, href } from './router.svelte';
  import { Command, Dialog } from 'bits-ui';
  import { palette, closeOverlays } from './keyboard.svelte';
  import Icon from './Icon.svelte';
  import type { Me, Page, Pull } from './types';
  import { getJSON } from './api.svelte';
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

  // Gated the same way as the settings navigation: the Configuration page
  // and each account's audit log are for admins, and "Sign in" only makes
  // sense when there's no session yet.
  function buildEntries(r: Route, searching: boolean): Entry[] {
    const entries: Entry[] = [{ label: 'Overview', hint: 'instance', words: true, route: { name: 'overview' }, icon: mdiViewGridOutline, keywords: 'all accounts' }];
    if ((me?.accounts.length ?? 0) > 1) {
      entries.push({ label: 'Queue', hint: 'instance', words: true, route: { name: 'instanceQueue' }, icon: mdiTrayFull, keywords: 'jobs slots all accounts' });
    }
    if (me?.admin) {
      entries.push({ label: 'Configuration', hint: 'instance', words: true, route: { name: 'console' }, icon: mdiConsoleLine, keywords: 'admin console settings' });
      if (searching) {
        for (const { label, target, keywords } of CONSOLE_SECTIONS) {
          entries.push({ label, hint: 'configuration', words: true, route: { name: 'console' }, icon: mdiCogOutline, target, keywords });
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
        { label: 'Analytics', hint: slug, route: { name: 'account', slug }, icon: mdiChartBoxOutline, keywords: 'overview reviews dashboard' },
        { label: 'Repositories', hint: slug, route: { name: 'repos', slug }, icon: mdiSourceRepository },
        { label: 'Pull requests', hint: slug, route: { name: 'pulls', slug }, icon: mdiSourcePull },
        { label: 'Findings', hint: slug, route: { name: 'findings', slug }, icon: mdiBugOutline, keywords: 'bugs caught addressed' },
        { label: 'Rules', hint: slug, route: { name: 'rules', slug }, icon: mdiScaleBalance, keywords: 'rules context kritika.yaml' },
        { label: 'Queue', hint: slug, route: { name: 'queue', slug }, icon: mdiTrayFull },
        { label: 'Spend', hint: slug, route: { name: 'usage', slug }, icon: mdiCurrencyUsd, keywords: 'usage cost tokens' },
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

  // The recently updated pulls of every account the viewer can read,
  // fetched each time the palette opens so they are jump targets too: the
  // current account's lead, then the rest, the latest first.
  let recent = $state<{ slug: string; pull: Pull }[]>([]);

  let recentSeq = 0;

  async function loadRecent(slugs: string[], current: string | undefined): Promise<void> {
    const seq = ++recentSeq;
    const pages = await Promise.all(
      slugs.map(async (slug) => {
        try {
          const p = await getJSON<Page<Pull>>(`${accountApi(slug)}/pulls?state=all&limit=20`);
          return p.items.map((pull) => ({ slug, pull }));
        } catch (err) {
          console.error('palette recent pulls:', err);
          return [];
        }
      }),
    );
    if (seq !== recentSeq) return;
    recent = pages
      .flat()
      .sort((a, b) => Number(b.slug === current) - Number(a.slug === current) || Date.parse(b.pull.updatedAt) - Date.parse(a.pull.updatedAt));
  }

  $effect(() => {
    const current = currentSlug(router.route);
    const slugs = [...new Set([...(current ? [current] : []), ...(me?.accounts ?? [])])];
    if (palette.open && slugs.length) void loadRecent(slugs, current);
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

  function commit(row: Entry | undefined): void {
    if (!row) return;
    closeOverlays();
    navigate(row.route);
    if (row.target) focusWhenShown(row.target);
  }
</script>

<Dialog.Root open={palette.open} onOpenChange={(now) => !now && closeOverlays()}>
  <Dialog.Portal>
    <Dialog.Overlay class="palette-overlay" />
    <Dialog.Content class="palette" aria-label="Go to">
      <!-- The rows are already the ones that match, so Bits UI's own filter is off; it keeps the cursor and the arrow keys. -->
      <Command.Root class="palette-command" shouldFilter={false} loop label="Go to">
        <div class="palette-input">
          <Icon path={mdiMagnify} size={16} />
          <Command.Input bind:value={q} placeholder="Go to…" aria-label="Go to" />
          <span class="palette-esc"><kbd>Esc</kbd></span>
        </div>

        <Command.List class="palette-body">
          {#each rows as row (`${row.label}\u0000${href(row.route)}`)}
            <Command.Item class="palette-row" value={`${row.label}\u0000${href(row.route)}`} onSelect={() => commit(row)}>
              <Icon path={row.icon} size={14} />
              <span class="row-main">
                <span class="row-title">{row.label}</span>
                {#if row.hint}<span class="row-sub" class:mono={!row.words}>{row.hint}</span>{/if}
              </span>
            </Command.Item>
          {/each}
          {#if q.trim() && !rows.length}
            <p class="palette-empty">Nothing matches “{q}”.</p>
          {/if}
        </Command.List>
      </Command.Root>

      <div class="palette-footer">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>⏎</kbd> open</span>
      </div>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
