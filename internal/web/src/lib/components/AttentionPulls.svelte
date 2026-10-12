<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { accountApi, pullRoute } from '../links';
  import { href } from '../router.svelte';
  import { Resource } from '../resource.svelte';
  import type { WANTS } from '../attention';
  import type { Page, Pull } from '../types';
  import Pill from './Pill.svelte';
  import StateView from './StateView.svelte';

  let { slug, want, count, version }: { slug: string; want: (typeof WANTS)[number]; count: number; version: number } = $props();
  const res = new Resource(() => {
    const params = new URLSearchParams({ state: 'open', limit: '3' });
    if (want.filter.outcome) params.set('outcome', want.filter.outcome);
    if (want.filter.is) params.set('is', want.filter.is);
    return getJSON<Page<Pull>>(`${accountApi(slug)}/pulls?${params}`);
  });
  let visible = $state(false);
  let requestedVersion = -1;
  $effect(() => {
    if (visible && requestedVersion !== version) {
      requestedVersion = version;
      void res.load();
    }
  });
  function observe(node: HTMLElement): () => void {
    const observer = new IntersectionObserver(([entry]) => { visible = !!entry?.isIntersecting; }, { rootMargin: '160px' });
    observer.observe(node);
    return () => observer.disconnect();
  }
</script>

<div class="attention-group" {@attach observe}>
  <div class="attention-group-head">
    <Pill tone={want.tone} label={want.label} />
    <span class="mono small muted">{slug}</span>
    <a class="small" href={href({ name: 'pulls', slug, filter: want.filter })} aria-label={`View all ${count} ${want.label} pull requests in ${slug}`}>View all {count} →</a>
  </div>
  <StateView {res} retry={() => res.load()} isEmpty={(d) => !d.items.length} empty="No matching open pull requests now.">
    {#snippet children(d)}
      {#each d.items as p (p.url)}
        {@const review = want.key !== 'paused' && p.lastReview}
        <a class="attention-pull" href={href(review ? { name: 'review', slug, id: review.id } : pullRoute(slug, p))}>
          <strong>{p.title}</strong>
          <span class="small muted"><span class="mono">{p.repository}#{p.number}</span> · {review ? 'Inspect review' : 'View pull request'} →</span>
        </a>
      {/each}
    {/snippet}
  </StateView>
</div>
