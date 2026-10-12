<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { getJSON } from '../../api.svelte';
  import { router, replace } from '../../router.svelte';
  import { Resource } from '../../resource.svelte';
  import { listKeys } from '../../listkeys';
  import { focusWhenShown } from '../../focus';
  import { SEVERITIES } from '../../format';
  import { parseDiff } from '../../diff';
  import type { ReviewDetail, ReviewDiff } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import FindingCard from './FindingCard.svelte';
  import DiffFileView from './DiffFileView.svelte';

  // Files this long start collapsed, as does every file once the diff as a
  // whole is this long, so a huge diff doesn't render thousands of rows up front.
  const FILE_LINES = 500;
  const TOTAL_LINES = 5000;

  // An empty response during preparation is not the final, immutable diff.
  let { base, d, version, finding, active = true }: { base: string; d: ReviewDetail; version: number; finding?: string; active?: boolean } = $props();
  const res = new Resource(() => getJSON<ReviewDiff>(`${base}/diff`));
  let delta = $state(false);
  $effect(() => {
    void version;
    untrack(() => {
      if (!res.data || res.data.pending) void res.load();
    });
  });

  const files = $derived(res.data ? parseDiff(delta && res.data.deltaDiff ? res.data.deltaDiff : res.data.diff) : []);
  const total = $derived(files.reduce((n, f) => n + f.lines.length, 0));
  const outside = $derived.by(() => {
    const paths = new Set(files.map((f) => f.path));
    return d.findings.filter((f) => !paths.has(f.path));
  });
  const findings = $derived([...d.findings].sort((a, b) => SEVERITIES.indexOf(a.severity) - SEVERITIES.indexOf(b.severity)));
  let query = $state('');
  let onlyFindings = $state(false);
  let search: HTMLInputElement;
  const findingsByPath = $derived.by(() => {
    const byPath = new Map<string, typeof findings>();
    for (const f of findings) byPath.set(f.path, [...(byPath.get(f.path) ?? []), f]);
    return byPath;
  });
  const visibleFiles = $derived(files.filter((file) => file.path.toLowerCase().includes(query.trim().toLowerCase()) && (!onlyFindings || findingsByPath.has(file.path))));
  const visiblePaths = $derived(new Set(visibleFiles.map((f) => f.path)));
  let selected = $state('');
  const selectedIndex = $derived(findings.findIndex((f) => f.id === selected));
  let followed: string | undefined;

  function step(offset: number): void {
    const f = findings[selectedIndex + offset];
    if (f) void showFinding(f.id);
  }
  let reveal = $state<{ path: string; serial: number }>();

  async function showFinding(id: string): Promise<void> {
    const f = findings.find((f) => f.id === id);
    if (!f) return;
    selected = id;
    followed = id;
    if (router.route.name === 'review' && router.route.tab === 'diff' && router.route.finding !== id) replace({ ...router.route, finding: id });
    if (!f.path.toLowerCase().includes(query.trim().toLowerCase())) query = '';
    reveal = { path: f.path, serial: (reveal?.serial ?? 0) + 1 };
    await tick();
    focusWhenShown(`#diff-finding-${CSS.escape(id)}`, true);
  }

  async function showFile(path: string): Promise<void> {
    selected = '';
    followed = undefined;
    if (router.route.name === 'review' && router.route.tab === 'diff' && router.route.finding) replace({ ...router.route, finding: undefined });
    reveal = { path, serial: (reveal?.serial ?? 0) + 1 };
    await tick();
    focusWhenShown(`#diff-file-${CSS.escape(path)}`, true);
  }

  $effect(() => {
    if (active && finding && res.data && !res.data.pending) untrack(() => {
      if (finding === followed) return;
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
      <span class="small muted">{visibleFiles.length} of {files.length} files</span>
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
    <div class="review-workspace">
      <nav class="finding-nav" aria-label="Findings and files">
        <h2>Findings <span class="tab-count">{findings.length}</span></h2>
        {#each findings as f (f.id)}
          <button class="finding-jump" class:active={selected === f.id} aria-pressed={selected === f.id} onclick={() => void showFinding(f.id)}>
            <span class="sev sev-{f.severity}">{f.severity}</span>
            <strong>{f.title}</strong>
            <span class="mono small muted">{f.path}:{f.line}</span>
          </button>
        {:else}<p class="state-msg">No findings.</p>{/each}
        <h2>Changed files <span class="tab-count">{files.length}</span></h2>
        <div class="file-filters">
          <label for="file-search">Search files</label>
          <input id="file-search" type="search" bind:this={search} bind:value={query} placeholder="Path or filename" />
          <label class="toggle"><input type="checkbox" bind:checked={onlyFindings} /> Files with findings</label>
        </div>
        {#each visibleFiles as file (file.path)}
          <button class="file-jump mono" onclick={() => void showFile(file.path)}>Open {file.path}</button>
        {:else}<p class="state-msg">No files match these filters.</p>{/each}
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
          <div hidden={!visiblePaths.has(file.path)}>
            <DiffFileView
              {file}
              {reveal}
              findings={findingsByPath.get(file.path) ?? []}
              pullUrl={d.review.pull.url}
              initiallyOpen={total <= TOTAL_LINES && file.lines.length <= FILE_LINES}
            />
          </div>
        {/each}
      </div>
    </div>
  {/snippet}
</StateView>
