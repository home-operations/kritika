<script lang="ts">
  import PageHeader from '../components/PageHeader.svelte';
  import { Switch } from 'bits-ui';
  import { href, replace } from '../router.svelte';
  import { Paged, pollJobs } from '../resource.svelte';
  import { indexTone } from '../format';
  import { repoRoute, accountApi, reindexPath, turnedOnPath, forgeName } from '../links';
  import { getJSON, sendJSON } from '../api.svelte';
  import { describe, sendEach, toastTally } from '../manage';
  import { isAdmin } from '../session.svelte';
  import { toast } from '../toast.svelte';
  import type { AccountDetail, RegisterResult, Repository, TurnOnRequest } from '../types';
  import StateView from '../components/StateView.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import Pill from '../components/Pill.svelte';
  import Time from '../components/Time.svelte';
  import ReviewStatusTile from '../components/ReviewStatusTile.svelte';
  import LoadMore from '../components/LoadMore.svelte';
  import RepoTraits from '../components/RepoTraits.svelte';
  import RowToggle from '../components/RowToggle.svelte';
  import { rowToggles } from '../rowclick';
  import RepoPreview from '../components/RepoPreview.svelte';
  import SortHeader from '../components/SortHeader.svelte';
  import IndexQueueStatus from '../components/IndexQueueStatus.svelte';
  import { Sorter } from '../sort.svelte';
  import Dialog from '../components/Dialog.svelte';
  import Icon from '../Icon.svelte';
  import { mdiCheck, mdiProgressClock, mdiAlertCircleOutline, mdiSwapHorizontal } from '../icons';
  import type { IndexRunStatus } from '../types';

  import { repoFilter, type RepoFilter } from '../routes';

  let { slug, filter }: { slug: string; filter?: RepoFilter } = $props();
  const tableId = $props.id();
  let expanded = $state<Record<string, boolean>>({});
  const search = $derived(filter?.q ?? '');
  function setFilter(patch: RepoFilter): void {
    replace({ name: 'repos', slug, filter: repoFilter({ ...filter, ...patch }) });
  }
  // Which repositories the list shows: by default those kritika can run,
  // neither archived nor forks but the forks turned on; or the forks, the
  // public ones, or the archived ones.
  const kind = $derived(filter?.type ?? '');
  let filterEl = $state<HTMLInputElement | undefined>(undefined);

  function clearFilter(): void {
    setFilter({ q: undefined });
    filterEl?.focus();
  }

  const base = $derived(`${accountApi(slug)}/repos`);
  const forge = $derived(forgeName(slug));
  const paged = new Paged<Repository>(
    (cursor) => `${base}?limit=100${kind ? `&type=${kind}` : ''}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
    (r) => r.id,
  );
  const res = paged.first;

  $effect(() => {
    void paged.load();
  });
  // A queued job's progress raises no event until its run starts, so the
  // list polls while any repository it shows is queued or waiting.
  const pending = $derived(paged.items.some((r) => r.index.job || r.index.backlog));
  $effect(() => (pending ? pollJobs(() => void paged.refresh()) : undefined));

  function visible(): Repository[] {
    const needle = search.trim().toLowerCase();
    const all = paged.items;
    return sorter.sort(needle ? all.filter((r) => r.fullName.toLowerCase().includes(needle)) : all);
  }

  const manage = $derived(isAdmin());
  // Full names picked for a bulk action.
  let selected = $state<string[]>([]);
  $effect(() => {
    void kind;
    selected = [];
  });
  let busy = $state(false);
  let confirmReindex = $state(false);

  const isOn = (r: Repository): boolean => r.enabled;
  const indexIcon: Record<IndexRunStatus, string> = {
    running: mdiProgressClock,
    completed: mdiCheck,
    failed: mdiAlertCircleOutline,
    superseded: mdiSwapHorizontal,
  };
  const plural = (n: number): string => (n === 1 ? '1 repository' : `${n} repositories`);

  // The Index column sorts by where each repository stands: indexing,
  // queued, waiting its turn to be queued, then by its last run.
  const runRank: Record<IndexRunStatus, number> = { running: 0, failed: 4, completed: 5, superseded: 6 };
  function indexRank(r: Repository): number {
    if (r.index.job) return r.index.job.state === 'running' ? 0 : 1;
    if (r.index.backlog) return 2 + r.index.backlog / 1e6;
    return r.index.lastRunStatus ? runRank[r.index.lastRunStatus] : 7;
  }
  // Sorting orders the repositories loaded so far; the server pages them by name.
  const sorter = new Sorter<'name' | 'enabled' | 'index' | 'review', Repository>({
    name: (r) => r.fullName,
    enabled: (r) => (isOn(r) ? 0 : 1),
    index: indexRank,
    review: (r) => r.lastReview ? Date.parse(r.lastReview.createdAt) : undefined,
  });

  function pick(fullName: string, on: boolean): void {
    selected = on ? [...selected, fullName] : selected.filter((n) => n !== fullName);
  }

  function pickAll(rows: Repository[], on: boolean): void {
    const names = rows.filter((r) => !r.archived).map((r) => r.fullName);
    selected = on ? [...new Set([...selected, ...names])] : selected.filter((n) => !names.includes(n));
  }

  // setEnabled turns each of names on or off, one request at a time: the
  // dashboard owns a repository's on or off.
  async function setEnabled(names: string[], on: boolean): Promise<void> {
    busy = true;
    const body: TurnOnRequest = { on };
    const tally = await sendEach(names, (name) => name, (name) => sendJSON('PUT', turnedOnPath(slug, name), body));
    busy = false;
    selected = selected.filter((n) => !names.includes(n));
    void paged.load();
    toastTally(tally, `${plural(tally.done)} turned ${on ? 'on' : 'off'}`);
  }

  // reindex queues a reindex of each selected repository that is on, one
  // request at a time.
  async function reindex(): Promise<void> {
    busy = true;
    const targets = paged.items.filter((r) => selected.includes(r.fullName) && isOn(r)).map((r) => r.fullName);
    const tally = await sendEach(targets, (name) => name, (name) => sendJSON('POST', reindexPath(slug, name)));
    busy = false;
    confirmReindex = false;
    selected = [];
    toastTally(tally, `Reindex queued for ${plural(tally.done)}`);
  }

  // resync lists the repositories the account's App or bot reaches again, which
  // records any newly archived, unarchived or added since.
  async function resync(): Promise<void> {
    busy = true;
    try {
      const d = await getJSON<AccountDetail>(accountApi(slug));
      const r = await sendJSON<RegisterResult>('POST', `/api/v1/admin/connections/${encodeURIComponent(d.connection.name)}/repositories`);
      toast(r?.added ? `Resynced from ${forge}: ${plural(r.added)} added` : `Resynced from ${forge}`);
      void paged.load();
    } catch (err) {
      toast(`Resync failed: ${describe(err)}`, 'danger');
    } finally {
      busy = false;
    }
  }

  function show(k: typeof kind): void {
    setFilter({ type: k || undefined });
  }

  const empty = $derived(kind === 'forks' ? 'No forks.' : kind === 'public' ? 'No public repositories.' : kind === 'archived' ? 'No archived repositories.' : 'No repositories yet.');

  const selectedOff = $derived(paged.items.filter((r) => selected.includes(r.fullName) && !isOn(r)).length);
</script>

<svelte:head><title>Repositories · {slug} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Repositories" {slug}>{#snippet actions()}{#if isAdmin()}<button class="btn" disabled={busy} onclick={resync}>Resync from {forge}</button>{/if}{/snippet}</PageHeader>
    <div class="toolbar flex flex-wrap items-center gap-2">
      <label class="search-box [flex:1_1_240px] min-w-0 flex">
        <span class="sr-only">Filter repositories</span>
        <input type="search" placeholder="Filter by name" bind:value={() => search, (q) => setFilter({ q })} bind:this={filterEl} />
      </label>
      <label class="select">
        <span class="sr-only">Type</span>
        <select bind:value={() => kind, show} aria-label="Type">
          <option value="">In use</option>
          <option value="forks">Forks</option>
          <option value="public">Public</option>
          <option value="archived">Archived</option>
        </select>
      </label>
      {#if manage && selected.length}
        <div class="bulk-actions flex flex-wrap items-center gap-1.5" role="group" aria-label="Selected repositories">
          <span class="small text-ui-sm">{selected.length} selected</span>
          <button class="btn btn-small" disabled={busy} onclick={() => setEnabled(selected, true)}>Turn on</button>
          <button class="btn btn-small" disabled={busy} onclick={() => setEnabled(selected, false)}>Turn off</button>
          <button class="btn btn-small" disabled={busy} onclick={() => (confirmReindex = true)}>Reindex…</button>
          <button class="btn btn-small" disabled={busy} onclick={() => (selected = [])}>Clear</button>
        </div>
      {/if}
    </div>
    {#if kind === 'forks'}
      <p class="small muted text-ui-sm">A fork is only reviewed and indexed once turned on here.</p>
    {:else if kind === 'public'}
      <p class="small muted text-ui-sm">Anyone on the internet can read the reviews, summaries and inline comments kritika posts on a public repository.</p>
    {:else if kind === 'archived'}
      <p class="small muted text-ui-sm">Archived repositories are inactive. Unarchive on {forge}, resync, then turn it on if it is off.</p>
    {:else}
      <p class="small muted text-ui-sm">
        Newly discovered repositories start off. Turn on the repositories you want reviewed and indexed. Their first indexes are queued a
        few at a time; the rest show as waiting, numbered in the order they will be queued.
      </p>
    {/if}
    <StateView {res} retry={() => paged.load()} isEmpty={(d) => d.items.length === 0} {empty}>
      {@const rows = visible()}
      {#if rows.length === 0}
        <div class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">
          <span>{paged.cursor ? `Nothing loaded yet matches “${search}”: load more to look further.` : `Nothing matches “${search}”.`}</span>
          <button class="btn btn-small" onclick={clearFilter}>Clear filter</button>
        </div>
      {:else}
        <div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
          <table class="data repo-table">
            <thead>
              <tr>
                {#if manage}
                  <th scope="col">
                    {#if rows.some((r) => !r.archived)}
                      <Checkbox
                        label="Select every repository shown"
                        checked={rows.every((r) => r.archived || selected.includes(r.fullName))}
                        indeterminate={!rows.every((r) => r.archived || selected.includes(r.fullName)) && rows.some((r) => !r.archived && selected.includes(r.fullName))}
                        onchange={(on) => pickAll(rows, on)}
                      />
                    {/if}
                  </th>
                {/if}
                <SortHeader label="Repository" key="name" {sorter} />
                <SortHeader label="Enabled" key="enabled" {sorter} />
                <SortHeader label="Index" key="index" {sorter} />
                <SortHeader label="Last review" key="review" {sorter} />
                <th scope="col" class="relative"><span class="sr-only">Preview</span></th>
              </tr>
            </thead>
            <tbody>
              {#each rows as repo, i (repo.id)}
                {@const key = `${slug}:${repo.id}`}
                {@const previewId = `${tableId}-${i}`}
                <tr class="row-toggles" class:row-expanded={expanded[key]} onclick={(e) => { if (rowToggles(e)) expanded[key] = !expanded[key]; }}>
                  {#if manage}
                    <td>
                      {#if !repo.archived}
                        <input
                          type="checkbox"
                          aria-label={`Select ${repo.fullName}`}
                          checked={selected.includes(repo.fullName)}
                          onchange={(e) => pick(repo.fullName, e.currentTarget.checked)}
                        />
                      {/if}
                    </td>
                  {/if}
                  <td class="mono"><a href={href(repoRoute(slug, repo.fullName))}>{repo.fullName}</a> <RepoTraits r={repo} /></td>
                  <td>
                    {#if manage}
                      <span class="toggle inline-flex items-center gap-1.5 [vertical-align:middle]" title={repo.archived ? `Archived on ${forge}: unarchive it there first` : undefined}>
                        <Switch.Root
                          class="switch relative flex-none w-7.5 h-4.5 p-0 border-0 rounded-[999px] [background:color-mix(in_srgb,_var(--muted)_70%,_var(--panel))] cursor-pointer [transition:background_0.15s]"
                          aria-label={`Review and index ${repo.fullName}`}
                          checked={isOn(repo)}
                          disabled={busy || repo.archived}
                          onCheckedChange={(on) => setEnabled([repo.fullName], on)}
                        >
                          <Switch.Thumb class="switch-thumb absolute top-0.5 left-0.5 w-3.5 h-3.5 rounded-[50%] bg-panel shadow-[0_1px_2px_color-mix(in_srgb,_var(--text)_30%,_transparent)] [transition:transform_0.15s]" />
                        </Switch.Root>
                        <span>{isOn(repo) ? 'On' : 'Off'}</span>
                      </span>
                    {:else if repo.enabled}<Pill tone="ok" label="on" />{:else}<Pill label="off" />{/if}
                  </td>
                  <td>
                    {#if repo.index.lastRunStatus}
                      {@const st = repo.index.lastRunStatus}
                      <span class="status tone-{indexTone[st]}">
                        <span class="status-tile inline-flex items-center justify-center w-4.5 h-4.5 rounded-sm text-[var(--tone,_var(--muted))] [background:color-mix(in_srgb,_var(--tone,_var(--muted))_14%,_transparent)]"><Icon path={indexIcon[st]} size={12} /></span>
                        <span class="status-word">{st}</span>
                      </span>
                      <Time iso={repo.index.lastRunAt} />
                    {:else if !repo.index.job && !repo.index.backlog}<span class="muted">never</span>{/if}
                    <IndexQueueStatus index={repo.index} />
                  </td>
                  <td>
                    {#if repo.lastReview}
                      <a href={href({ name: 'review', slug, id: repo.lastReview.id })}><ReviewStatusTile status={repo.lastReview.status} /></a>
                      <Time iso={repo.lastReview.createdAt} />
                    {:else}<span class="muted">—</span>{/if}
                  </td>
                  <td class="row-toggle-cell"><RowToggle bind:open={expanded[key]} label={`Preview ${repo.fullName}`} controls={previewId} /></td>
                </tr>
                {#if expanded[key]}
                  <tr class="row-preview"><td colspan={5 + Number(manage)}>
                    <div class="row-preview-body" id={previewId} role="region" aria-label={`Preview of ${repo.fullName}`}><RepoPreview {slug} {repo} /></div>
                  </td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
      <LoadMore {paged} />
    </StateView>
  </div>
</main>

<Dialog bind:open={confirmReindex} title={`Reindex ${plural(selected.length - selectedOff)}?`}>
  <p>
    Each is indexed again from its default branch, which spends embedder tokens.
    {#if selectedOff}{plural(selectedOff)} that {selectedOff === 1 ? 'is' : 'are'} off {selectedOff === 1 ? 'is' : 'are'} skipped.{/if}
  </p>
  {#snippet footer()}
    <button class="btn" onclick={() => (confirmReindex = false)}>Cancel</button>
    <button class="btn btn-primary" disabled={busy || selected.length === selectedOff} onclick={reindex}>{busy ? 'Queuing…' : 'Reindex'}</button>
  {/snippet}
</Dialog>
