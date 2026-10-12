<script lang="ts">
  // Maps the active route to its page. Keyed so switching account or record
  // starts the page fresh, while switching review tabs or list filters
  // keeps the page mounted.
  import type { Route } from '../routes';
  import { router } from '../router.svelte';
  import { rememberScroll } from '../viewstate';
  import Overview from './Overview.svelte';
  import Console from './Console.svelte';
  import Analytics from './Analytics.svelte';
  import Repos from './Repos.svelte';
  import Repo from './Repo.svelte';
  import Pulls from './Pulls.svelte';
  import Pull from './Pull.svelte';
  import Review from './review/Review.svelte';
  import Queue from './Queue.svelte';
  import InstanceQueue from './InstanceQueue.svelte';
  import Preferences from './Preferences.svelte';
  import Findings from './Findings.svelte';
  import Rules from './Rules.svelte';
  import Usage from './Usage.svelte';
  import Followups from './Followups.svelte';
  import Admin from './admin/Admin.svelte';

  let { route }: { route: Route } = $props();

  const LIST_PAGES: readonly Route['name'][] = ['overview', 'account', 'repos', 'queue', 'instanceQueue', 'pulls', 'findings', 'followups', 'usage'];

  function keyOf(r: Route): string {
    if (r.name === 'review') return JSON.stringify({ n: r.name, s: r.slug, id: r.id });
    if (LIST_PAGES.includes(r.name)) return JSON.stringify({ n: r.name, s: 'slug' in r ? r.slug : undefined });
    return JSON.stringify(r);
  }
  const key = $derived(keyOf(route));
</script>

<!-- data-route exposes the parsed route to the router tests. -->
<div class="route-host" data-route={JSON.stringify(route)} {@attach rememberScroll(LIST_PAGES.includes(route.name) ? key : '', router.entry, router.traversal)}>
{#key key}
  {#if route.name === 'overview'}
    <Overview />
  {:else if route.name === 'console'}
    <Console />
  {:else if route.name === 'instanceQueue'}
    <InstanceQueue filter={route.filter} />
  {:else if route.name === 'preferences'}
    <Preferences />
  {:else if route.name === 'account'}
    <Analytics slug={route.slug} filter={route.filter} />
  {:else if route.name === 'repos'}
    <Repos slug={route.slug} filter={route.filter} />
  {:else if route.name === 'repo'}
    <Repo slug={route.slug} owner={route.owner} repo={route.repo} />
  {:else if route.name === 'pulls'}
    <Pulls slug={route.slug} filter={route.filter} />
  {:else if route.name === 'pull'}
    <Pull slug={route.slug} owner={route.owner} repo={route.repo} number={route.number} />
  {:else if route.name === 'review'}
    <Review slug={route.slug} id={route.id} tab={route.tab} finding={route.finding} />
  {:else if route.name === 'findings'}
    <Findings slug={route.slug} filter={route.filter} />
  {:else if route.name === 'rules'}
    <Rules slug={route.slug} />
  {:else if route.name === 'queue'}
    <Queue slug={route.slug} filter={route.filter} />
  {:else if route.name === 'usage'}
    <Usage slug={route.slug} filter={route.filter} />
  {:else if route.name === 'followups'}
    <Followups slug={route.slug} />
  {:else if route.name === 'admin'}
    <Admin slug={route.slug} section={route.section} />
  {/if}
{/key}
</div>
