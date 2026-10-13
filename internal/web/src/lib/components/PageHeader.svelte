<script lang="ts">
  import type { Snippet } from 'svelte';
  import { returnToList } from '../router.svelte';
  import { router, href, type Route } from '../router.svelte';

  let { title, slug, parents = [], actions }: {
    title: string;
    slug?: string;
    parents?: { label: string; route: Route }[];
    actions?: Snippet;
  } = $props();
</script>

<header class="page-head flex flex-col gap-2">
  {#if router.origin}
    <a class="back-to-list text-[length:13px] mb-1" href={router.origin.hash} onclick={(event) => { if (!event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey) { event.preventDefault(); returnToList(); } }}>← Back to {router.origin.label}</a>
  {/if}
  <!-- The scope switcher already names the account; a crumb earns its row only past it. -->
  {#if parents.length}
    <nav class="crumbs text-ui-sm text-muted" aria-label="Breadcrumb">
      {#if slug}<a class="mono" href={href({ name: 'account', slug })}>{slug}</a>
      {:else}<span>All accounts</span>{/if}
      {#each parents as parent (href(parent.route))}<span aria-hidden="true">/</span><a href={href(parent.route)}>{parent.label}</a>{/each}
    </nav>
  {/if}
  <div class="page-title-row flex items-center justify-between flex-wrap gap-3"><h1>{title}</h1>{#if actions}<div class="page-actions">{@render actions()}</div>{/if}</div>
</header>
