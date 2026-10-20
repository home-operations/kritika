<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { sendJSON } from '../api.svelte';
  import { isAdmin } from '../session.svelte';
  import { sendEach, toastTally } from '../manage';
  import { navigate, replace } from '../router.svelte';
  import { PULL_IS, PULL_OUTCOMES, pullFilter, type PullFilter } from '../routes';
  import { Paged, Resource } from '../resource.svelte';
  import { pullKey, pullRoute, accountApi, rerunPath } from '../links';
  import { listKeys } from '../listkeys';
  import { formatTokens, type Parsed, type TokenSpec } from '../tokensearch';
  import type { Page, Pull, Repository } from '../types';
  import StateView from '../components/StateView.svelte';
  import PullTable from '../components/PullTable.svelte';
  import LoadMore from '../components/LoadMore.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import Segmented from '../components/Segmented.svelte';
  import TokenSearch from '../components/TokenSearch.svelte';
  import Dialog from '../components/Dialog.svelte';

  let { slug, filter }: { slug?: string; filter?: PullFilter } = $props();

  type ScopedPull = Pull & { account?: string };
  const accountOf = (p: ScopedPull): string => slug ?? p.account!;
  const keyOf = (p: ScopedPull): string => `${accountOf(p)}:${pullKey(p)}`;
  const listRoute = (filter?: PullFilter) => slug ? { name: 'pulls' as const, slug, filter } : { name: 'instancePulls' as const, filter };

  const prState = $derived(filter?.state ?? 'open');
  let searchEl = $state<HTMLInputElement | undefined>(undefined);

  // setFilter replaces the history entry, so Back leaves the list instead of
  // stepping through its filters.
  function setFilter(f: Parameters<typeof pullFilter>[0]): void {
    replace(listRoute(pullFilter(f)));
  }

  function clearFilters(): void {
    replace(listRoute());
    searchEl?.focus();
  }

  const account = $derived(slug ? accountApi(slug) : '/api/v1');

  function query(after?: string): string {
    const p = new URLSearchParams({ state: prState, limit: '50' });
    for (const k of ['outcome', 'is', 'repo', 'author', 'q'] as const) {
      const v = filter?.[k];
      if (v) p.set(k, v);
    }
    if (after) p.set('cursor', after);
    return `${account}/pulls?${p}`;
  }

  const paged = new Paged<ScopedPull>(query, keyOf);
  const res = paged.first;
  const repos = Resource.json<Page<Repository>>(() => `${account}/repos?limit=100`);
  const repoNames = $derived((repos.data?.items ?? []).map((r) => r.fullName));

  $effect(() => {
    void paged.load();
  });
  $effect(() => {
    if (slug) void repos.load();
  });

  // The box's tokens: status: is the last review's outcome.
  const authors = $derived([...new Set(paged.items.map((p) => p.author))].sort());
  const specs = $derived<TokenSpec[]>([
    { key: 'repo', hint: 'a repository', values: repoNames, open: true },
    { key: 'author', hint: "an author's login", values: authors, open: true },
    { key: 'status', hint: "the last review's status", values: PULL_OUTCOMES },
    { key: 'is', hint: 'paused, or p0', values: PULL_IS },
  ]);
  const boxText = (f: PullFilter | undefined) => formatTokens(specs, { repo: f?.repo, author: f?.author, status: f?.outcome, is: f?.is }, f?.q);

  // written is the box's text for the filter it last wrote to the URL: any
  // other change to the filter, such as following a link to the unfiltered
  // list, rewrites the box.
  let text = $state(untrack(() => boxText(filter)));
  let written = untrack(() => boxText(filter));
  function onapply(p: Parsed): void {
    const f = { state: filter?.state, repo: p.tokens.repo, author: p.tokens.author, outcome: p.tokens.status, is: p.tokens.is, q: p.q };
    written = boxText(pullFilter(f));
    setFilter(f);
  }
  $effect(() => {
    const t = boxText(filter);
    if (t === written) return;
    written = t;
    text = t;
  });

  const items = $derived(paged.items);
  // The cursor follows its pull rather than its position, which a live
  // refetch shifts when it adds or reorders rows above it.
  let selectedKey = $state('');
  const selected = $derived(items.findIndex((p) => keyOf(p) === selectedKey));

  async function select(i: number): Promise<void> {
    const p = items[i];
    if (!p) return;
    selectedKey = keyOf(p);
    await tick();
    document.querySelector(`.pull-rows [data-index="${i}"]`)?.scrollIntoView({ block: 'nearest' });
  }

  $effect(() =>
    listKeys({
      count: () => items.length,
      get: () => selected,
      set: (i) => void select(i),
      open: (i) => {
        const p = items[i];
        if (p) navigate(pullRoute(accountOf(p), p));
      },
      focusSearch: () => searchEl?.focus(),
      toggle: (i) => {
        const p = items[i];
        if (p && p.state === 'open' && slug && isAdmin()) pick([keyOf(p)], !picked.includes(keyOf(p)));
      },
    }),
  );

  // An admin picks pull requests to re-run together, by checkbox or Space.
  // A pick holds under the filter it was made in: another filter shows
  // other rows, so it starts with none picked.
  const filterKey = $derived(JSON.stringify(filter ?? {}));
  let pickedUnder = $state({ filter: '', keys: [] as string[] });
  const picked = $derived(pickedUnder.filter === filterKey ? pickedUnder.keys : []);
  let confirmRerun = $state(false);
  let busy = $state(false);
  function pick(keys: string[], on: boolean): void {
    pickedUnder = { filter: filterKey, keys: on ? [...new Set([...picked, ...keys])] : picked.filter((k) => !keys.includes(k)) };
  }

  const plural = (n: number) => (n === 1 ? '1 pull request' : `${n} pull requests`);

  // rerunPicked queues a fresh review of each picked pull request at its
  // current head, one request at a time.
  async function rerunPicked(): Promise<void> {
    busy = true;
    const targets = items.filter((p) => picked.includes(keyOf(p)));
    const tally = await sendEach(targets, keyOf, (p) => sendJSON('POST', rerunPath(accountOf(p), p)));
    busy = false;
    confirmRerun = false;
    pick(picked, false);
    toastTally(tally, `Re-run queued for ${plural(tally.done)}`, 'already queued or running');
    void paged.load();
  }

  const STATES = [
    { value: 'open', label: 'Open' },
    { value: 'closed', label: 'Closed' },
    { value: 'all', label: 'All' },
  ] as const;
</script>

<svelte:head><title>Pull requests · {slug ?? 'All accounts'} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Pull requests" {slug} />
    <div class="toolbar flex flex-wrap items-center gap-2" role="search">
      <TokenSearch
        id="pull-search"
        label="Search pull requests"
        placeholder="Search pull requests or add filters…"
        {specs}
        bind:text
        bind:input={searchEl}
        {onapply}
      />
      <Segmented label="State" options={STATES} value={prState} onchange={(state) => setFilter({ ...filter, state })} />
      {#if picked.length}
        <div class="bulk-actions flex flex-wrap items-center gap-1.5" role="group" aria-label="Selected pull requests">
          <span class="small text-ui-sm">{picked.length} selected</span>
          <button class="btn btn-small" disabled={busy} onclick={() => (confirmRerun = true)}>Re-run…</button>
          <button class="btn btn-small" disabled={busy} onclick={() => pick(picked, false)}>Clear</button>
        </div>
      {/if}
    </div>
    <StateView {res} retry={() => paged.load()}>
      {#if items.length === 0 && filter}
        <div class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">
          <span>No pull requests match these filters.</span>
          <button class="btn btn-small" onclick={clearFilters}>Clear filters</button>
        </div>
      {:else if items.length === 0}
        <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">{prState === 'all' ? 'No pull requests.' : `No ${prState} pull requests.`}</p>
      {:else}
        <PullTable {slug} {items} {selected} {picked} {keyOf} onpick={slug && isAdmin() ? pick : undefined} />
        <LoadMore {paged} />
      {/if}
    </StateView>
  </div>
</main>

<Dialog bind:open={confirmRerun} title={`Re-run ${plural(picked.length)}?`}>
  <p>Each is reviewed again at its current head, which spends model tokens. One already queued or running is left as it is.</p>
  {#snippet footer()}
    <button class="btn" onclick={() => (confirmRerun = false)}>Cancel</button>
    <button class="btn btn-primary" disabled={busy} onclick={rerunPicked}>{busy ? 'Queuing…' : 'Re-run'}</button>
  {/snippet}
</Dialog>
