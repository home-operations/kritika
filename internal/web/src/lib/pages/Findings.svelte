<script lang="ts">
  // Every finding of the account, once per pull request however many of
  // its reviews repeated it, and whether a later review found it addressed
  // or a maintainer dismissed it.
  import { tick, untrack } from 'svelte';
  import { getJSON } from '../api.svelte';
  import { href, replace } from '../router.svelte';
  import { FINDING_STATUSES, findingFilter, type FindingFilter } from '../routes';
  import { Paged, Resource, errorMessage, live } from '../resource.svelte';
  import { accountApi, pullRoute, threadUrl } from '../links';
  import { CATEGORIES, SEVERITIES } from '../format';
  import { formatTokens, type Parsed, type TokenSpec } from '../tokensearch';
  import Dialog from '../components/Dialog.svelte';
  import FindingCard from './review/FindingCard.svelte';
  import { isTyping } from '../keyboard.svelte';
  import { listKeys } from '../listkeys';
  import type { AccountFinding, Page, Repository } from '../types';
  import StateView from '../components/StateView.svelte';
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
  function query(after?: string): string {
    const p = new URLSearchParams({ limit: '50' });
    for (const k of ['severity', 'category', 'status', 'repo', 'rule', 'q'] as const) {
      const v = filter?.[k];
      if (v) p.set(k, v);
    }
    if (after) p.set('cursor', after);
    return `${account}/findings?${p}`;
  }
  const paged = new Paged<AccountFinding>(query, (f) => f.id);
  const res = paged.first;
  const repos = new Resource(() => getJSON<Page<Repository>>(`${account}/repos?limit=100`));
  $effect(() => {
    void paged.load();
  });
  $effect(() => {
    void repos.load();
  });
  $effect(() => live((e) => e.account === slug && e.kind === 'review', () => void paged.refresh()));

  const specs = $derived<TokenSpec[]>([
    { key: 'repo', hint: 'a repository', values: (repos.data?.items ?? []).map((r) => r.fullName), open: true },
    { key: 'severity', hint: 'p0, p1 or p2', values: SEVERITIES },
    { key: 'category', hint: 'what kind of problem', values: CATEGORIES },
    { key: 'status', hint: 'open, addressed or dismissed', values: FINDING_STATUSES },
    { key: 'rule', hint: 'a rule id', values: [...new Set(paged.items.flatMap((f) => f.rules))].sort(), open: true },
  ]);
  const boxText = (f: FindingFilter | undefined) =>
    formatTokens(specs, { repo: f?.repo, severity: f?.severity, category: f?.category, status: f?.status, rule: f?.rule }, f?.q);

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
    <PageHeader title="Findings" {slug} />
    <div class="toolbar flex flex-wrap items-center gap-2" role="search">
      <TokenSearch
        id="finding-search"
        label="Search findings"
        placeholder="Search, or filter by repo:, severity:, category:, status: or rule:"
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
    <StateView {res} retry={() => paged.load()}>
      {#if paged.items.length === 0 && filter}
        <div class="state-msg flex items-center gap-2 m-0 p-3 text-muted">
          <span>No findings match these filters.</span>
          <button class="btn btn-small" onclick={clearFilters}>Clear filters</button>
        </div>
      {:else if paged.items.length === 0}
        <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">No findings yet: they appear here as reviews report them.</p>
      {:else}
        <div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
          <table class="data finding-table">
            <thead>
              <tr>
                <th scope="col">Finding</th>
                <th scope="col">Severity · kind</th>
                <th scope="col">Pull request</th>
                <th scope="col" title="Addressed once a later review of the pull request no longer reports it; dismissed when a maintainer replied so in its thread">Status</th>
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
                    {#if f.rules.length}
                      <span class="finding-rules flex flex-wrap gap-1 mt-1">
                        {#each f.rules as id (id)}
                          <a class="mono" href={href({ name: 'findings', slug, filter: { rule: id } })} title="Findings that cite the rule {id}">{id}</a>
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
                      {#if threadUrl(f.pull.url, f.forgeCommentId)}
                        <a class="external" href={threadUrl(f.pull.url, f.forgeCommentId)} target="_blank" rel="noopener noreferrer" title="The finding's thread on GitHub">
                          <Icon path={mdiOpenInNew} size={11} label="Thread on GitHub" />
                        </a>
                      {/if}
                    </span>
                  </td>
                  <td>
                    <span class="status tone-{statusTone[f.status]}">
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
    <p class="muted small key-hints text-ui-sm m-0"><kbd>j</kbd>/<kbd>k</kbd> move · <kbd>⏎</kbd> open · <kbd>/</kbd> search</p>
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
      {#key inspected.id}<FindingCard f={inspected} pullUrl={inspected.pull.url} />{/key}
      <div class="workspace-actions flex items-center flex-wrap gap-2.5">
        <a class="btn btn-primary" href={href({ ...reviewOf(inspected), tab: 'diff' })}>Inspect in code →</a>
        <a class="btn" href={href(reviewOf(inspected))}>Open review</a>
      </div>
      <p class="small muted text-ui-sm">First found <Time iso={inspected.firstSeenAt} /> · Last seen <Time iso={inspected.lastSeenAt} /></p>
      <p class="small muted text-ui-sm"><kbd>j</kbd>/<kbd>k</kbd> browse · <kbd>Esc</kbd> return to findings</p>
    </div>
  {/if}
</Dialog>
