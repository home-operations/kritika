<script lang="ts">
  import type { ReviewDetail, Severity } from '../../types';
  import { SEVERITIES, isActive } from '../../format';
  import Markdown from '../../components/Markdown.svelte';
  import Confidence from '../../components/Confidence.svelte';
  import FindingCard from './FindingCard.svelte';
  import Collapsible from '../../components/Collapsible.svelte';
  import HelpPopover from '../../components/HelpPopover.svelte';
  import RowToggle from '../../components/RowToggle.svelte';
  import { rowToggles } from '../../rowclick';

  import { focusWhenShown } from '../../focus';
  import type { ReviewTab, Route } from '../../routes';
  import { href } from '../../router.svelte';

  // finding is the id of the finding a link led here for.
  // slug is the account, for the links to a rule's findings.
  let { slug, d, finding, oninspect, to }: { slug: string; d: ReviewDetail; finding?: string; oninspect: () => void; to: (tab: ReviewTab, finding?: string) => Route } = $props();
  let expanded = $state<Record<string, boolean>>({});
  $effect(() => {
    if (finding) {
      expanded[finding] = true;
      focusWhenShown(`#finding-${CSS.escape(finding)}`, true);
    }
  });
  const findings = $derived(
    SEVERITIES.flatMap((s: Severity) => d.findings.filter((f) => f.severity === s)),
  );
</script>

{#if d.summary}
  <section class="panel" aria-labelledby="sum-take">
    <header class="panel-head flex-wrap"><h2 id="sum-take">Review summary</h2>
      {#if d.review.confidence}
        <div class="flex items-center flex-wrap gap-2 text-ui-sm text-muted">
          {#if d.review.confidence.risk}<span>{d.review.confidence.risk} risk</span>{/if}
          <HelpPopover label={`Confidence ${d.review.confidence.score}/5${d.review.confidence.passed ? '' : ' · below threshold'}`}><Confidence c={d.review.confidence} /></HelpPopover>
        </div>
      {/if}
    </header>
    <div class="panel-body py-3 px-4">
      {#if d.summary.headline}<p class="headline mt-0 mx-0 mb-1.5 font-semibold leading-[1.5]">{d.summary.headline}</p>{/if}
      <Markdown text={d.summary.take} />
    </div>
  </section>
{:else}
  <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No summary{isActive(d.review.status) ? ' yet' : ''}.</p>
{/if}

{#if findings.length === 0}
  <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">{d.review.status === 'completed' ? 'No findings.' : isActive(d.review.status) ? 'Findings will appear as the review progresses.' : d.review.status === 'skipped' ? 'No findings recorded. This review was skipped.' : 'No findings recorded. The review did not complete.'}</p>
{:else}
  <section class="panel" aria-label="Findings">
    <header class="panel-head"><h2>Findings</h2><span class="text-ui-sm text-muted">{findings.length} reported</span></header>
    <div class="table-wrap overflow-x-auto max-w-full">
      <table class="data review-findings">
        <thead><tr><th scope="col">Severity</th><th scope="col">Finding</th><th scope="col">Status</th><th scope="col" class="relative"><span class="sr-only">Details</span></th></tr></thead>
        <tbody>
          {#each findings as f (f.id)}
            <tr class="row-toggles" class:row-expanded={expanded[f.id]} data-finding={f.id} onclick={(e) => { if (rowToggles(e)) expanded[f.id] = !expanded[f.id]; }}>
              <td><span class="sev sev-{f.severity}">{f.severity}</span></td>
              <td class="finding-summary-cell">
                <a class="finding-code-link font-semibold" aria-label={`${f.title}: Inspect ${f.path}:${f.line} in code`} onclick={(e) => { if (e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey) oninspect(); }} href={href(to('diff', f.id))}>{f.title}</a>
                <span class="block mono text-ui-xs text-muted wrap-anywhere">{f.path}:{f.line}</span>
              </td>
              <td class="text-ui-sm text-muted">{f.status}</td>
              <td class="row-toggle-cell"><RowToggle bind:open={expanded[f.id]} label={`Details for ${f.title}`} controls={`finding-${f.id}`} /></td>
            </tr>
            {#if expanded[f.id]}
              <tr class="row-preview"><td colspan="4"><div class="row-preview-body"><FindingCard {f} {slug} pullUrl={d.review.pull.url} id={`finding-${f.id}`} /></div></td></tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  </section>
{/if}

{#if d.summary?.praise.length}
  <Collapsible title="Positive notes">
    <ul class="praise m-0 pl-4.5">
      {#each d.summary.praise as p, i (i)}<li><Markdown text={p} /></li>{/each}
    </ul>
  </Collapsible>
{/if}

<!-- The rules this review was given: if it cites one nowhere, it made sure that the rule was kept. -->
{#if d.contextPack?.ruleIds.length}
  {@const cited = (id: string) => d.findings.filter((f) => f.rules.includes(id)).length}
  <section aria-label="Rules checked">
    <Collapsible title="Rules checked">
      {#snippet meta()}<span class="text-ui-sm text-muted">{d.contextPack!.ruleIds.length}</span>{/snippet}
    <ul class="rule-checks list-none m-0 p-0 flex flex-wrap gap-y-2 gap-x-4.5">
      {#each d.contextPack.ruleIds as id (id)}
        {@const n = cited(id)}
        <li>
          <a class="badge mono inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" href={href({ name: 'findings', slug, filter: { rule: id } })} title="Every finding that cites {id}">{id}</a>
          <span class="small text-ui-sm" class:muted={!n}>{n ? `${n} ${n === 1 ? 'finding' : 'findings'}` : 'no finding'}</span>
        </li>
      {/each}
    </ul>
    </Collapsible>
  </section>
{/if}
