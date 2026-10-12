<script lang="ts">
  import type { Snippet } from 'svelte';
  import { href, type Route } from '../router.svelte';

  let { title, slug, description, parents = [], actions }: {
    title: string;
    slug?: string;
    description?: string;
    parents?: { label: string; route: Route }[];
    actions?: Snippet;
  } = $props();
</script>

<header class="page-head">
  <nav class="crumbs" aria-label="Breadcrumb">
    {#if slug}<a href={href({ name: 'overview' })}>All accounts</a><span aria-hidden="true">/</span><a class="mono" href={href({ name: 'account', slug })}>{slug}</a>
    {:else}<span>All accounts</span>{/if}
    {#each parents as parent}<span aria-hidden="true">/</span><a href={href(parent.route)}>{parent.label}</a>{/each}
  </nav>
  <div class="page-title-row"><h1>{title}</h1>{#if actions}<div class="page-actions">{@render actions()}</div>{/if}</div>
  {#if description}<p class="muted page-description">{description}</p>{/if}
</header>
