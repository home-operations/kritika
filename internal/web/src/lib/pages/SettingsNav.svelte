<script lang="ts">
  // The navigation beside a settings page: an account's pages in its scope,
  // or, in the instance's, the Configuration page with its parts under it.
  import { href } from '../router.svelte';
  import type { Route } from '../routes';
  import { session } from '../session.svelte';
  import { scopeOf } from '../sections';
  import { focusWhenShown } from '../focus';
  import { CONSOLE_SECTIONS, type SettingEntry } from '../settingsindex';
  import Icon from '../Icon.svelte';
  import { mdiSourceRepository, mdiClipboardTextClockOutline, mdiConsoleLine } from '../icons';

  let { route }: { route: Route } = $props();

  const slug = $derived(scopeOf(route));
  const admin = $derived(session.me?.admin === true);

  interface Item {
    label: string;
    icon: string;
    to: Route;
    active: boolean;
    // parts are the page's sections, listed while it is shown.
    parts?: readonly SettingEntry[];
  }

  const items = $derived.by((): { group: string; mono: boolean; items: Item[] }[] => {
    const groups: { group: string; mono: boolean; items: Item[] }[] = [];
    if (slug) {
      const account: Item[] = [
        { label: 'Repositories', icon: mdiSourceRepository, to: { name: 'repos', slug }, active: route.name === 'repos' || route.name === 'repo' },
      ];
      if (admin) {
        account.push({
          label: 'Audit log',
          icon: mdiClipboardTextClockOutline,
          to: { name: 'admin', slug, section: 'audit' },
          active: route.name === 'admin',
        });
      }
      groups.push({ group: slug, mono: true, items: account });
    }
    if (admin && !slug) {
      groups.push({
        group: 'Instance',
        mono: false,
        items: [{ label: 'Configuration', icon: mdiConsoleLine, to: { name: 'console' }, active: route.name === 'console', parts: CONSOLE_SECTIONS }],
      });
    }
    return groups;
  });
</script>

<nav class="subnav" aria-label="Settings">
  {#each items as g (g.group)}
    <p class="subnav-group" class:mono={g.mono}>{g.group}</p>
    {#each g.items as it (it.label)}
      <a class="subnav-item" class:active={it.active} aria-current={it.active ? 'page' : undefined} href={href(it.to)}>
        <Icon path={it.icon} size={15} />
        {it.label}
      </a>
      {#if it.active && it.parts}
        <ul class="subnav-parts" aria-label="{it.label} sections">
          {#each it.parts as p (p.target)}
            <li><a class="subnav-part" href={href(it.to)} onclick={() => focusWhenShown(p.target, true)}>{p.label}</a></li>
          {/each}
        </ul>
      {/if}
    {/each}
  {/each}
</nav>
