<script lang="ts">
  import type { ReviewDetail, Severity } from '../../types';
  import { SEVERITIES, isActive } from '../../format';
  import Markdown from '../../components/Markdown.svelte';
  import ReviewImpact from './ReviewImpact.svelte';
  import Confidence from '../../components/Confidence.svelte';
  import FindingCard from './FindingCard.svelte';

  import { focusWhenShown } from '../../focus';
  import type { ReviewTab, Route } from '../../routes';
  import { href } from '../../router.svelte';

  // finding is the id of the finding a link led here for.
  // slug is the account, for the links to a rule's findings.
  let { slug, d, finding, oninspect, to }: { slug: string; d: ReviewDetail; finding?: string; oninspect: () => void; to: (tab: ReviewTab, finding?: string) => Route } = $props();
  $effect(() => {
    if (finding) focusWhenShown(`#finding-${CSS.escape(finding)}`, true);
  });
  const groups = $derived(
    SEVERITIES.map((s: Severity) => ({ s, items: d.findings.filter((f) => f.severity === s) })).filter((g) => g.items.length),
  );
</script>

{#if d.summary}
  <section class="panel" aria-labelledby="sum-take">
    <header class="panel-head"><h2 id="sum-take">Take</h2></header>
    <div class="panel-body">
      {#if d.summary.headline}<p class="headline">{d.summary.headline}</p>{/if}
      {#if d.review.confidence}<Confidence c={d.review.confidence} />{/if}
      <Markdown text={d.summary.take} />
    </div>
    {#if d.summary.praise.length}
      <h3 class="subhead">Praise</h3>
      <ul class="praise">
        {#each d.summary.praise as p, i (i)}<li><Markdown text={p} /></li>{/each}
      </ul>
    {/if}
  </section>
{:else}
  <p class="state-msg">No summary{isActive(d.review.status) ? ' yet' : ''}.</p>
{/if}

{#if d.summary?.impact?.length}
  <section class="panel" aria-labelledby="sum-impact">
    <header class="panel-head"><h2 id="sum-impact">Merge impact</h2></header>
    <div class="panel-body">
      <ReviewImpact notes={d.summary.impact} />
    </div>
  </section>
{/if}

{#if groups.length === 0}
  <p class="state-msg">{d.review.status === 'completed' ? 'No findings.' : isActive(d.review.status) ? 'Findings will appear as the review progresses.' : d.review.status === 'skipped' ? 'No findings recorded. This review was skipped.' : 'No findings recorded. The review did not complete.'}</p>
{/if}
{#each groups as g (g.s)}
  <section class="finding-group" aria-labelledby="sev-{g.s}">
    <h2 id="sev-{g.s}" class="finding-group-head"><span class="sev sev-{g.s}">{g.s}</span> {g.items.length}</h2>
    {#each g.items as f (f.id)}
      <FindingCard {f} pullUrl={d.review.pull.url} id={`finding-${f.id}`} />
      <a class="finding-code-link" onclick={(e) => { if (e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey) oninspect(); }} href={href(to('diff', f.id))}>Inspect {f.path}:{f.line} in code →</a>
    {/each}
  </section>
{/each}

<!-- The rules this review was given: one it cites nowhere was checked and found kept. -->
{#if d.contextPack?.ruleIds.length}
  {@const cited = (id: string) => d.findings.filter((f) => f.rules.includes(id)).length}
  <section class="panel" aria-labelledby="sum-rules">
    <header class="panel-head">
      <h2 id="sum-rules">Rules checked</h2>
      <span class="small muted">{d.contextPack.ruleIds.length} applied to this change</span>
    </header>
    <ul class="rule-checks">
      {#each d.contextPack.ruleIds as id (id)}
        {@const n = cited(id)}
        <li>
          <a class="badge mono" href={href({ name: 'findings', slug, filter: { rule: id } })} title="Every finding that cites {id}">{id}</a>
          <span class="small" class:muted={!n}>{n ? `${n} ${n === 1 ? 'finding' : 'findings'}` : 'no finding'}</span>
        </li>
      {/each}
    </ul>
  </section>
{/if}
