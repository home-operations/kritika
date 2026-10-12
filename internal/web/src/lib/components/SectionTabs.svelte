<script lang="ts">
  import { SECTIONS, SUB_TABS, type Section } from '../sections';
  import type { Route } from '../routes';

  let { section, slug, current }: { section: Section; slug: string; current: Route['name'] } = $props();
  const title = $derived(SUB_TABS[section]?.find((t) => t.name === current)?.label ?? SECTIONS[section].label);
</script>

<header class="page-head section-head">
  <p class="crumbs mono">{slug}</p>
  <h1>{title}</h1>
</header>
