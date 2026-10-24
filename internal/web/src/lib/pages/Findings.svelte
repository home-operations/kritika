<script lang="ts">
  // Every finding of the account, once per pull request however many of
  // its reviews repeated it, and whether a later review found it addressed
  // or a maintainer dismissed it.
  import { tick, untrack } from 'svelte';
  import { href, replace } from '../router.svelte';
  import { DISMISS_SOURCES, FINDING_IS, FINDING_STATUSES, findingFilter, type FindingFilter } from '../routes';
  import { Paged, Resource, errorMessage } from '../resource.svelte';
  import { basePath } from '../base';
  import { accountApi, pullRoute, threadUrl, forgeName } from '../links';
  import { CATEGORIES, DISMISSED_BY, SEVERITIES } from '../format';
  import { formatTokens, type Parsed, type TokenSpec } from '../tokensearch';
  import Dialog from '../components/Dialog.svelte';
  import FindingCard from './review/FindingCard.svelte';
  import { isTyping } from '../keyboard.svelte';
  import { listKeys } from '../listkeys';
  import type { AccountFinding, FindingSummary as Summary, Page, Repository } from '../types';
  import StateView from '../components/StateView.svelte';
  import FindingSummary, { type GroupKind } from '../components/FindingSummary.svelte';
  import HelpPopover from '../components/HelpPopover.svelte';
  import LoadMore from '../components/LoadMore.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import TokenSearch from '../components/TokenSearch.svelte';
  import Segmented from '../components/Segmented.svelte';
  import Reactions from '../components/Reactions.svelte';
  import Time from '../components/Time.svelte';
  import Icon from '../Icon.svelte';
  import { mdiCancel, mdiCheck, mdiCircleOutline, mdiOpenInNew } from '../icons';

  const statusIcon = { open: mdiCircleOutline, addressed: mdiCheck, dismissed: mdiCancel } as const;
  // An open finding is the one still asking for something.
  const statusTone = { open: 'accent', addressed: 'ok', dismissed: 'muted' } as const;
  const STATUSES = [
    { value: '', label: 'All' },
    { value: 'open', label: 'Open' },
    { value: 'addressed', label: 'Addressed' },
    { value: 'dismissed', label: 'Dismissed' },
  ] as const;

  let { slug, filter }: { slug: string; filter?: FindingFilter } = $props();

  const account = $derived(accountApi(slug));
  // filterParams is the filter as the findings API reads it, after extra.
  function filterParams(extra: Record<string, string> = {}): URLSearchParams {
    const p = new URLSearchParams(extra);
    for (const k of ['severity', 'category', 'status', 'dismissed', 'repo', 'rule', 'agent', 'path', 'is', 'q'] as const) {
      const v = filter?.[k];
      if (v) p.set(k, v);
    }
    return p;
  }
  function query(after?: string): string {
    const p = filterParams({ limit: '50' });
    if (after) p.set('cursor', after);
    return `${account}/findings?${p}`;
  }
  const exportHref = (format: 'markdown' | 'json') => `${basePath}${account}/findings/export?${filterParams({ format })}`;
  const paged = new Paged<AccountFinding>(query, (f) => f.id);
  const res = paged.first;
  const repos = Resource.json<Page<Repository>>(() => `${account}/repos?limit=100`);
  const summary = Resource.live<Summary>(() => `${account}/findings/summary?${filterParams()}`);
  let summaryKind = $state<GroupKind>('categories');
  $effect(() => {
    void paged.load();
  });
  $effect(() => {
    void repos.load();
  });
  $effect(() => {
    void summary.load();
  });

  const specs = $derived<TokenSpec[]>([
    { key: 'repo', hint: 'a repository', values: (repos.data?.items ?? []).map((r) => r.fullName), open: true },
    { key: 'severity', hint: 'p0, p1 or p2', values: SEVERITIES },
    { key: 'category', hint: 'what kind of problem', values: CATEGORIES },
    { key: 'status', hint: 'open, addressed or dismissed', values: FINDING_STATUSES },
    { key: 'dismissed', hint: 'by command, resolved or conceded', values: DISMISS_SOURCES },
    { key: 'rule', hint: 'a rule id', values: [...new Set(paged.items.flatMap((f) => f.rules))].sort(), open: true },
    // A finding no named agent raised is the default agent's, so the default agent is offered only with a named one on the page;
    // a agent filter in force keeps its token whatever the page holds, so editing the box does not drop it.
    ...(filter?.agent || paged.items.some((f) => f.agents.length)
      ? [{
          key: 'agent',
          hint: 'a agent name',
          values: [...new Set([...paged.items.flatMap((f) => (f.agents.length ? f.agents : ['default'])), ...(filter?.agent ? [filter.agent] : [])])].sort(),
          open: true,
        }]
      : []),
    // No suggestions: a suggestion would replace a path typed in another case, which names another path.
    { key: 'path', hint: 'a path the findings are under', values: [], open: true },
    { key: 'is', hint: 'downvoted: its comment drew a thumbs down', values: FINDING_IS },
  ]);
  const boxText = (f: FindingFilter | undefined) =>
    formatTokens(
      specs,
      { repo: f?.repo, severity: f?.severity, category: f?.category, status: f?.status, dismissed: f?.dismissed, rule: f?.rule, agent: f?.agent, path: f?.path, is: f?.is },
      f?.q,
    );

  // As on the pull request list: written is the box's text for the filter it
  // last wrote to the URL, and any other change to the filter rewrites it.
  let searchEl = $state<HTMLInputElement | undefined>(undefined);
  let text = $state(untrack(() => boxText(filter)));
  let written = untrack(() => boxText(filter));
  function onapply(p: Parsed): void {
    const f = findingFilter({ ...p.tokens, q: p.q });
    written = boxText(f);
    replace({ name: 'findings', slug, filter: f });
  }
  $effect(() => {
    const t = boxText(filter);
    if (t === written) return;
    written = t;
    text = t;
  });

  function clearFilters(): void {
    replace({ name: 'findings', slug });
    searchEl?.focus();
  }

  const reviewOf = (f: AccountFinding) => ({ name: 'review' as const, slug, id: f.reviewId, finding: f.id });

  // The cursor follows its finding, as the pull request list's follows its
  // pull request.
  let selectedId = $state('');
  let inspectedId = $state('');
  let inspector = $state<HTMLDivElement>();
  const inspected = $derived(paged.items.find((f) => f.id === inspectedId));
  const inspectedIndex = $derived(paged.items.findIndex((f) => f.id === inspectedId));
  $effect(() => { void filter; inspectedId = ''; });
  const selected = $derived(paged.items.findIndex((f) => f.id === selectedId));
  async function select(i: number): Promise<void> {
    const f = paged.items[i];
    if (!f) return;
    selectedId = f.id;
    await tick();
    document.querySelector(`.finding-table [data-index="${i}"]`)?.scrollIntoView({ block: 'nearest' });
  }
  $effect(() =>
    listKeys({
      count: () => paged.items.length,
      get: () => selected,
      set: (i) => void select(i),
      open: (i) => {
        const f = paged.items[i];
        if (f) inspect(f);
      },
      focusSearch: () => searchEl?.focus(),
    }),
  );

  function inspect(f: AccountFinding): void {
    selectedId = f.id;
    inspectedId = f.id;
  }

  async function step(offset: number): Promise<void> {
    if (paged.loadingMore) return;
    const from = inspectedId;
    const focused = document.activeElement;
    if (offset > 0 && inspectedIndex === paged.items.length - 1 && paged.cursor) await paged.more();
    if (!inspectedId || inspectedId !== from) return;
    const f = paged.items[inspectedIndex + offset];
    if (f) inspect(f);
    await tick();
    if (focused instanceof HTMLElement && focused.isConnected && !(focused instanceof HTMLButtonElement && focused.disabled)) focused.focus({ preventScroll: true });
    else inspector?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus({ preventScroll: true });
  }

  function panelKey(e: KeyboardEvent): void {
    if (!inspected || !(e.target instanceof Element) || !e.target.closest('.finding-inspector')) return;
    if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === 'j') void step(1);
    else if (e.key === 'k') void step(-1);
    else return;
    e.preventDefault();
    e.stopPropagation();
  }

  function onTitleClick(e: MouseEvent, f: AccountFinding): void {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    inspect(f);
  }

  function onRowClick(e: MouseEvent, f: AccountFinding): void {
    if ((e.target as Element).closest('a, button') || getSelection()?.toString()) return;
    inspect(f);
  }
</script>

<svelte:window onkeydown={panelKey} />

<svelte:head><title>Findings · {slug} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Findings" {slug}>
      {#snippet actions()}
        <a class="btn" href={exportHref('markdown')} download>Export Markdown</a>
        <a class="btn" href={exportHref('json')} download>Export JSON</a>
        <HelpPopover label="About exports">
          <p>
            An export holds the findings these filters keep, newest first, up to 1,000. Give the Markdown to a coding agent to
            suggest AGENTS.md, skill or rule changes; the JSON has every field the list has.
          </p>
        </HelpPopover>
      {/snippet}
    </PageHeader>
    <div class="toolbar flex flex-wrap items-center gap-2" role="search">
      <TokenSearch
        id="finding-search"
        label="Search findings"
        placeholder="Search findings or add filters…"
        {specs}
        bind:text
        bind:input={searchEl}
        {onapply}
      />
      <Segmented
        label="Status"
        options={STATUSES}
        value={filter?.status ?? ''}
        onchange={(status) => replace({ name: 'findings', slug, filter: findingFilter({ ...filter, status }) })}
      />
    </div>
    {#if summary.data?.findings}
      <FindingSummary {slug} {filter} summary={summary.data} bind:kind={summaryKind} />
    {:else if summary.error}
      <p class="error-text text-danger text-ui-sm m-0" role="alert">
        Could not load the common findings: {errorMessage(summary.error)} <button class="btn" onclick={() => void summary.load()}>Retry</button>
      </p>
    {/if}
    <StateView {res} retry={() => paged.load()}>
      {#if paged.items.length === 0 && filter}
        <div class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">
          <span>No findings match these filters.</span>
          <button class="btn btn-small" onclick={clearFilters}>Clear filters</button>
        </div>
      {:else if paged.items.length === 0}
        <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No findings yet: they appear here as reviews report them.</p>
      {:else}
        <div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
          <table class="data finding-table">
            <thead>
              <tr>
                <th scope="col">Finding</th>
                <th scope="col">Severity · kind</th>
                <th scope="col">Pull request</th>
                <th scope="col" title="Addressed once a later review of the pull request no longer reports it; dismissed with the dismiss command, by resolving its thread, or when kritika conceded it there">Status</th>
                <th scope="col" class="num">Found</th>
              </tr>
            </thead>
            <tbody>
              {#each paged.items as f, i (f.id)}
                <!-- The title opens the inspector; the row is a larger pointer target. -->
                <tr class="finding-row cursor-pointer" class:selected={i === selected} data-index={i} onclick={(e) => onRowClick(e, f)}>
                  <td class="finding-main">
                    <a class="finding-link block overflow-hidden text-ellipsis text-ink text-ui-md [font-weight:550]" href={href(reviewOf(f))} aria-current={i === selected ? 'true' : undefined} aria-haspopup="dialog" onclick={(e) => onTitleClick(e, f)}>{f.title}</a>
                    <span class="finding-sub block mt-0.5 overflow-hidden text-ellipsis text-ui-xs text-muted">{f.explanation}</span>
                    {#if f.rules.length || f.agents.length}
                      <span class="finding-rules flex flex-wrap gap-1 mt-1">
                        {#each f.rules as id (id)}
                          <a class="mono" href={href({ name: 'findings', slug, filter: { rule: id } })} title="Findings that cite the rule {id}">{id}</a>
                        {/each}
                        {#each f.agents as name (name)}
                          <a class="mono" href={href({ name: 'findings', slug, filter: { agent: name } })} title="Raised by the {name} agent">{name}</a>
                        {/each}
                      </span>
                    {/if}
                  </td>
                  <td>
                    <span class="sev sev-{f.severity}">{f.severity}</span>
                    {#if f.category}<a class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" href={href({ name: 'findings', slug, filter: { category: f.category } })} title="Findings of this kind">{f.category}</a>{/if}
                  </td>
                  <td class="finding-pull">
                    <a class="mono" href={href(pullRoute(slug, f.pull))} title={f.pull.title}>{f.pull.repository}#{f.pull.number}</a>
                    <span class="finding-sub mono block mt-0.5 overflow-hidden text-ellipsis text-ui-xs text-muted">
                      {f.path}:{f.line}
                      {#if threadUrl(slug, f.pull.url, f.forgeCommentId)}
                        <a class="external" href={threadUrl(slug, f.pull.url, f.forgeCommentId)} target="_blank" rel="noopener noreferrer" title="The finding's thread on {forgeName(slug)}">
                          <Icon path={mdiOpenInNew} size={11} label="Thread on {forgeName(slug)}" />
                        </a>
                      {/if}
                    </span>
                  </td>
                  <td>
                    <span class="status tone-{statusTone[f.status]}" title={f.dismissSource ? DISMISSED_BY[f.dismissSource] : undefined}>
                      <span class="status-tile inline-flex items-center justify-center w-4.5 h-4.5 rounded-sm text-[var(--tone,_var(--muted))] [background:color-mix(in_srgb,_var(--tone,_var(--muted))_14%,_transparent)]"><Icon path={statusIcon[f.status]} size={12} /></span>
                      <span class="status-word">{f.status}</span>
                    </span>
                    <Reactions up={f.reactionsUp} down={f.reactionsDown} />
                    {#if f.dismissReason}<span class="finding-sub finding-dismissed block mt-0.5 overflow-hidden text-ellipsis text-ui-xs text-muted max-w-55" title="The reason it was dismissed with">{f.dismissReason}</span>{/if}
                  </td>
                  <td class="num"><Time iso={f.firstSeenAt} /></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <LoadMore {paged} />
      {/if}
    </StateView>
  </div>
</main>


<Dialog open={!!inspected} title="Finding details" side onclose={() => (inspectedId = '')} returnFocus=".finding-row.selected .finding-link">
  {#if inspected}
    <div class="finding-inspector flex flex-col gap-4 min-w-0 wrap-anywhere" bind:this={inspector} role="group" aria-label="Finding inspection">
      <div class="finding-controls flex items-center flex-wrap gap-y-2 gap-x-4" role="group" aria-label="Browse findings">
        <button class="btn" disabled={paged.loadingMore || inspectedIndex <= 0} onclick={() => void step(-1)}>← Previous</button>
        <span class="small muted text-ui-sm" aria-live="polite">{inspectedIndex + 1} of {paged.items.length} loaded</span>
        <button class="btn" disabled={paged.loadingMore || (inspectedIndex >= paged.items.length - 1 && !paged.cursor)} onclick={() => void step(1)}>Next →</button>
        <button class="btn" onclick={() => (inspectedId = '')}>Close</button>
      </div>
      {#if paged.moreError}<p class="error-text text-danger text-ui-sm whitespace-pre-wrap [word-break:break-word] m-0" role="alert">Could not load more findings: {errorMessage(paged.moreError)} <button class="btn" onclick={() => void step(1)}>Retry</button></p>{/if}
      <p><a class="mono" href={href(pullRoute(slug, inspected.pull))}>{inspected.pull.repository}#{inspected.pull.number}</a> · {inspected.pull.title}</p>
      {#key inspected.id}<FindingCard f={inspected} {slug} pullUrl={inspected.pull.url} />{/key}
      <div class="workspace-actions flex items-center flex-wrap gap-2.5">
        <a class="btn btn-primary" href={href({ ...reviewOf(inspected), tab: 'diff' })}>Inspect in code →</a>
        <a class="btn" href={href(reviewOf(inspected))}>Open review</a>
      </div>
      <p class="small muted text-ui-sm">First found <Time iso={inspected.firstSeenAt} /> · Last seen <Time iso={inspected.lastSeenAt} /></p>
      <p class="small muted text-ui-sm"><kbd>j</kbd>/<kbd>k</kbd> browse · <kbd>Esc</kbd> return to findings</p>
    </div>
  {/if}
</Dialog>
