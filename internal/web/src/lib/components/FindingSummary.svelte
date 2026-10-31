<script module lang="ts">
  import type { FindingCounts, FindingSummary } from '../types';
  // GroupKind is what the summary groups findings by.
  export type GroupKind = Exclude<keyof FindingSummary, keyof FindingCounts>;
</script>

<script lang="ts">
  // The findings the list's filters keep, counted, and the most common
  // categories, rules, agents and directories among them, each narrowing
  // the list to itself: what recurs, to answer in AGENTS.md or a skill,
  // and what is dismissed or drew a 👎, to answer in a rule.
  import { href } from '../router.svelte';
  import { findingFilter, type FindingFilter } from '../routes';
  import { wholeNumber } from '../format';
  import type { FindingGroup } from '../types';
  import Segmented from './Segmented.svelte';
  import Meter from './Meter.svelte';
  import Icon from '../Icon.svelte';
  import { mdiThumbDownOutline } from '../icons';

  // kind is bindable so that the page keeps it while a new filter's
  // summary loads.
  let { slug, filter, summary, kind = $bindable('categories') }: { slug: string; filter?: FindingFilter; summary: FindingSummary; kind?: GroupKind } = $props();

  const KINDS = [
    { value: 'categories', label: 'Categories', column: 'Category', none: 'No finding has a category.' },
    { value: 'rules', label: 'Rules', column: 'Rule', none: 'No finding cites a rule.' },
    { value: 'agents', label: 'Agents', column: 'Agent', none: 'No finding names an agent.' },
    { value: 'directories', label: 'Directories', column: 'Directory', none: 'No finding names a file.' },
  ] as const;
  const shown = $derived(KINDS.find((k) => k.value === kind)!);
  const groups = $derived(summary[kind]);

  // here is a directory group of the files in no directory below the path
  // filter, which a path cannot narrow to.
  const here = (g: FindingGroup) => kind === 'directories' && g.key === (filter?.path ?? '');
  // blocked says why no filter keeps g's findings alone. The list filters
  // by one rule and one agent, so with one set, another rule's or agent's
  // group holds only the findings that cite both. The search box splits a
  // path that holds a space.
  function blocked(g: FindingGroup): string | undefined {
    if (kind === 'rules' && filter?.rule) return 'The list already filters by a rule';
    if (kind === 'agents' && filter?.agent) return 'The list already filters by an agent';
    if (here(g)) return `Files in no directory below ${g.key || 'the root'}`;
    if (kind === 'directories' && /\s/.test(g.key)) return 'The search box cannot hold a path with a space';
  }
  // narrowed is the list's filter narrowed to g too: a directory by its
  // path in its repository, which lists the directories below it.
  function narrowed(g: FindingGroup): FindingFilter | undefined {
    switch (kind) {
      case 'categories': return findingFilter({ ...filter, category: g.key });
      case 'rules': return findingFilter({ ...filter, rule: g.key });
      case 'agents': return findingFilter({ ...filter, agent: g.key });
      case 'directories': return findingFilter({ ...filter, repo: g.repository, path: g.key });
    }
  }
  // A directory names its repository unless the filter already does.
  function name(g: FindingGroup): string {
    if (kind !== 'directories') return g.key;
    const dir = here(g) ? (g.key ? `${g.key} (files)` : 'root') : g.key || 'root';
    return filter?.repo ? dir : `${g.repository} · ${dir}`;
  }
</script>

<section class="panel finding-summary" aria-labelledby="finding-summary-title">
  <header class="panel-head flex-wrap">
    <div class="flex flex-col gap-0.5 min-w-0">
      <h2 id="finding-summary-title">Common findings</h2>
      <span class="finding-totals small muted text-ui-sm">
        {wholeNumber(summary.findings)} {summary.findings === 1 ? 'finding' : 'findings'} · {wholeNumber(summary.addressed)} addressed ·
        {wholeNumber(summary.dismissed)} dismissed ·
        <span class="inline-flex items-center gap-0.5">{wholeNumber(summary.downvoted)} <Icon path={mdiThumbDownOutline} size={12} label="thumbs down" /></span>
      </span>
    </div>
    <Segmented label="Group findings by" options={KINDS} value={kind} onchange={(v) => (kind = v)} />
  </header>
  {#if groups.length === 0}
    <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">{shown.none}</p>
  {:else}
    <div class="table-wrap overflow-x-auto max-w-full">
      <table class="data finding-groups">
        <thead>
          <tr>
            <th scope="col">{shown.column}</th>
            <th scope="col" class="num" title="Findings in the group, each counted once per pull request">Findings</th>
            <th scope="col" class="num" title="Of those, the ones a later review of the pull request no longer reported">Addressed</th>
            <th scope="col" class="num" title="Of those, the ones dismissed in their threads">Dismissed</th>
            <th scope="col" class="num" title="Of those, the ones whose comment drew a thumbs down on GitHub"><span class="inline-flex"><Icon path={mdiThumbDownOutline} size={12} label="Thumbs down" /></span></th>
          </tr>
        </thead>
        <tbody>
          {#each groups as g (`${g.repository}\u0000${g.key}`)}
            {@const why = blocked(g)}
            <tr>
              <td class={['name-fill', { mono: kind !== 'categories' }]} title={why ?? name(g)}>
                {#if why}<span class="muted">{name(g)}</span>{:else}<a href={href({ name: 'findings', slug, filter: narrowed(g) })}>{name(g)}</a>{/if}
              </td>
              <td class="num">
                {wholeNumber(g.findings)}
                <Meter value={g.findings} max={summary.findings} label="Share of the findings" color="var(--chart-ink)" />
              </td>
              <td class="num">{wholeNumber(g.addressed)}</td>
              <td class="num">{wholeNumber(g.dismissed)}</td>
              <td class="num">{wholeNumber(g.downvoted)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>
