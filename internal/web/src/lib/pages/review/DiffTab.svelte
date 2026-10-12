<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { getJSON } from '../../api.svelte';
  import { router, replace } from '../../router.svelte';
  import { Resource } from '../../resource.svelte';
  import { listKeys } from '../../listkeys';
  import { focusWhenShown } from '../../focus';
  import { parseDiff } from '../../diff';
  import type { Finding, ReviewDetail, ReviewDiff } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import FindingCard from './FindingCard.svelte';
  import DiffFileView from './DiffFileView.svelte';

  // Files this long start collapsed, as does every file once the diff as a
  // whole is this long, so a huge diff doesn't render thousands of rows up front.
  const FILE_LINES = 500;
  const TOTAL_LINES = 5000;

  // An empty response during preparation is not the final, immutable diff.
  let {
    base, d, version, finding, active = true, selected = $bindable(''), inspection,
  }: {
    base: string; d: ReviewDetail; version: number; finding?: string;
    active?: boolean; selected?: string; inspection: number;
  } = $props();
  const res = new Resource(() => getJSON<ReviewDiff>(`${base}/diff`));
  let delta = $state(false);
  let navigatorShown = $state(true);
  $effect(() => {
    void version;
    if (!active) return;
    untrack(() => {
      if (!res.data || res.data.pending) void res.load();
    });
  });

  const files = $derived(res.data ? parseDiff(delta && res.data.deltaDiff ? res.data.deltaDiff : res.data.diff) : []);
  const total = $derived(files.reduce((n, f) => n + f.lines.length, 0));
  const findings = $derived(d.findings);
  // A type change can have deletion and creation entries for the same path.
  // Own each finding once, preferring the entry that contains its new-side line.
  const owners = $derived.by(() => {
    const paths = new Map<string, number[]>();
    files.forEach((file, i) => paths.set(file.path, [...(paths.get(file.path) ?? []), i]));
    const owner = new Map<string, number>();
    for (const f of findings) {
      const candidates = paths.get(f.path) ?? [];
      const i = candidates.find((i) => files[i]!.newPath === f.path && files[i]!.lines.some((l) => l.newNo === f.line))
        ?? candidates.find((i) => files[i]!.newPath === f.path) ?? candidates[0];
      if (i !== undefined) owner.set(f.id, i);
    }
    return owner;
  });
  const findingsByFile = $derived.by(() => {
    const groups = new Map<number, Finding[]>();
    for (const f of findings) {
      const i = owners.get(f.id);
      if (i !== undefined) groups.set(i, [...(groups.get(i) ?? []), f]);
    }
    return groups;
  });
  const outside = $derived(findings.filter((f) => !owners.has(f.id)));
  let query = $state('');
  let onlyFindings = $state(false);
  let search = $state<HTMLInputElement>();
  let open = $state<boolean[]>([]);
  $effect.pre(() => { open = files.map((f) => total <= TOTAL_LINES && f.lines.length <= FILE_LINES); });
  const visibleFiles = $derived(files.map((file, i) => ({ file, i })).filter(({ file, i }) => file.path.toLowerCase().includes(query.trim().toLowerCase()) && (!onlyFindings || findingsByFile.has(i))));
  const visibleIndexes = $derived(new Set(visibleFiles.map(({ i }) => i)));
  const fileId = (i: number) => `diff-file-${delta ? 'delta' : 'full'}-${i}`;
  const selectedIndex = $derived(findings.findIndex((f) => f.id === selected));
  let followed: string | undefined;
  let followedInspection = -1;

  function step(offset: number): void {
    const f = findings[selectedIndex + offset];
    if (f) void showFinding(f.id);
  }

  async function showFinding(id: string): Promise<void> {
    const f = findings.find((f) => f.id === id);
    if (!f) return;
    selected = id;
    followed = id;
    followedInspection = inspection;
    if ((router.route.name === 'review' || router.route.name === 'pull') && router.route.tab === 'diff' && router.route.finding !== id) replace({ ...router.route, finding: id });
    if (!f.path.toLowerCase().includes(query.trim().toLowerCase())) query = '';
    const i = owners.get(id);
    if (i !== undefined) open[i] = true;
    await tick();
    focusWhenShown(`#diff-finding-${CSS.escape(id)}`, true);
  }

  async function showFile(i: number): Promise<void> {
    selected = '';
    followed = undefined;
    if ((router.route.name === 'review' || router.route.name === 'pull') && router.route.tab === 'diff' && router.route.finding) replace({ ...router.route, finding: undefined });
    open[i] = true;
    await tick();
    focusWhenShown(`#${fileId(i)}`, true);
  }

  $effect(() => {
    void inspection;
    if (active && finding && res.data && !res.data.pending) untrack(() => {
      if (finding === followed && inspection === followedInspection) return;
      followed = finding;
      void showFinding(finding);
    });
  });
  $effect(() => {
    void delta;
    untrack(() => {
      if (selected && active) void showFinding(selected);
    });
  });
  $effect(() =>
    active ? listKeys({
      count: () => findings.length,
      focusSearch: () => search?.focus(),
      get: () => findings.findIndex((f) => f.id === selected),
      set: (i) => { const f = findings[i]; if (f) void showFinding(f.id); },
      open: (i) => { const f = findings[i]; if (f) void showFinding(f.id); },
    }) : undefined,
  );
</script>

<StateView {res} retry={() => res.load()}>
  {#snippet children(x)}
    {#if !x.diff && !x.deltaDiff}<p class="state-msg">{x.pending ? 'Preparing the diff…' : x.swept ? 'The diff was not kept past the retention window.' : 'No diff recorded for this review.'}</p>{/if}
    <div class="toolbar">
      <button class="btn" aria-expanded={navigatorShown} aria-controls="review-navigator" onclick={() => (navigatorShown = !navigatorShown)}>{navigatorShown ? 'Hide navigator' : 'Show navigator'}</button>
      {#if files.length}<span class="small muted">{visibleFiles.length} of {files.length} files</span>{/if}
      {#if x.deltaDiff}
        <div class="view-toggle" role="group" aria-label="Diff scope">
          <button class:active={!delta} aria-pressed={!delta} onclick={() => (delta = false)}>Full</button>
          <button class:active={delta} aria-pressed={delta} onclick={() => (delta = true)}>Since last review</button>
        </div>
      {/if}
    </div>
    <div class="finding-controls" role="group" aria-label="Finding navigation">
      <button class="btn" disabled={selectedIndex <= 0} onclick={() => step(-1)}>← Previous finding</button>
      <span class="small muted" aria-live="polite">{selectedIndex < 0 ? `${findings.length} findings` : `Finding ${selectedIndex + 1} of ${findings.length}`}</span>
      <button class="btn" disabled={!findings.length || selectedIndex === findings.length - 1} onclick={() => step(1)}>Next finding →</button>
    </div>
    <div class="review-workspace" class:code-focused={!navigatorShown}>
      <nav id="review-navigator" class="finding-nav" aria-label="Findings and files" hidden={!navigatorShown}>
        <h2>Findings <span class="tab-count">{findings.length}</span></h2>
        {#each findings as f (f.id)}
          <button class="finding-jump" class:active={selected === f.id} aria-pressed={selected === f.id} onclick={() => void showFinding(f.id)}>
            <span class="sev sev-{f.severity}">{f.severity}</span>
            <strong>{f.title}</strong>
            <span class="mono small muted">{f.path}:{f.line}</span>
          </button>
        {:else}<p class="state-msg">No findings.</p>{/each}
        <h2>Changed files <span class="tab-count">{files.length}</span></h2>
        {#if files.length}
          <div class="file-filters">
            <label for="file-search">Search files</label>
            <input id="file-search" type="search" bind:this={search} bind:value={query} placeholder="Path or filename" />
            <label class="toggle"><input type="checkbox" bind:checked={onlyFindings} /> Files with findings</label>
          </div>
          {#each visibleFiles as { file, i } (i)}
            <button class="file-jump mono" onclick={() => void showFile(i)}>Open {file.path}{!file.newPath ? ' (deleted)' : !file.oldPath ? ' (added)' : ''}</button>
          {:else}<p class="state-msg">No files match these filters.</p>{/each}
        {:else}<p class="state-msg">{x.pending ? 'Changed files will appear when the diff is ready.' : x.swept ? 'Changed files are unavailable after diff retention expires.' : 'No changed files recorded.'}</p>{/if}
        <p class="small muted finding-nav-help"><kbd>j</kbd> / <kbd>k</kbd> navigate findings</p>
      </nav>
      <div class="review-code">
        {#if outside.length}
          <section class="panel" aria-label="Findings outside this diff">
            <header class="panel-head"><h2>Outside this diff</h2></header>
            {#each outside as f (f.id)}<FindingCard {f} pullUrl={d.review.pull.url} id={`diff-finding-${f.id}`} />{/each}
          </section>
        {/if}
        {#each files as file, i (`${delta}:${i}`)}
          <div hidden={!visibleIndexes.has(i)}>
            <DiffFileView
              {file}
              id={fileId(i)}
              bind:open={open[i]}
              findings={findingsByFile.get(i) ?? []}
              pullUrl={d.review.pull.url}
            />
          </div>
        {/each}
      </div>
    </div>
  {/snippet}
</StateView>
