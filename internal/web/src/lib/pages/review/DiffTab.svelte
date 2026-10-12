<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { getJSON } from '../../api.svelte';
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
  let { base, d, version, finding }: { base: string; d: ReviewDetail; version: number; finding?: string } = $props();
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
  let selected = $state('');
  let reveal = $state<{ path: string; serial: number }>();

  async function showFinding(id: string): Promise<void> {
    const f = findings.find((f) => f.id === id);
    if (!f) return;
    selected = id;
    reveal = { path: f.path, serial: (reveal?.serial ?? 0) + 1 };
    await tick();
    focusWhenShown(`#diff-finding-${CSS.escape(id)}`, true);
  }

  async function showFile(path: string): Promise<void> {
    selected = '';
    reveal = { path, serial: (reveal?.serial ?? 0) + 1 };
    await tick();
    focusWhenShown(`#diff-file-${CSS.escape(path)}`, true);
  }

  $effect(() => {
    if (finding && res.data && !res.data.pending) untrack(() => void showFinding(finding));
  });
  $effect(() => {
    void delta;
    untrack(() => {
      if (selected) void showFinding(selected);
    });
  });
  $effect(() =>
    listKeys({
      count: () => findings.length,
      get: () => findings.findIndex((f) => f.id === selected),
      set: (i) => { const f = findings[i]; if (f) void showFinding(f.id); },
      open: (i) => { const f = findings[i]; if (f) void showFinding(f.id); },
    }),
  );
</script>

<StateView {res} retry={() => res.load()}>
  {#snippet children(x)}
    {#if !x.diff && !x.deltaDiff}<p class="state-msg">{x.pending ? 'Preparing the diff…' : x.swept ? 'The diff was not kept past the retention window.' : 'No diff recorded for this review.'}</p>{/if}
    <div class="toolbar">
      <span class="small muted">{files.length} file{files.length === 1 ? '' : 's'}</span>
      {#if x.deltaDiff}
        <div class="view-toggle" role="group" aria-label="Diff scope">
          <button class:active={!delta} aria-pressed={!delta} onclick={() => (delta = false)}>Full</button>
          <button class:active={delta} aria-pressed={delta} onclick={() => (delta = true)}>Since last review</button>
        </div>
      {/if}
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
        {#each files as file (file.path)}
          <button class="file-jump mono" onclick={() => void showFile(file.path)}>Open {file.path}</button>
        {/each}
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
          <DiffFileView
            {file}
            {reveal}
            findings={d.findings.filter((f) => f.path === file.path)}
            pullUrl={d.review.pull.url}
            initiallyOpen={total <= TOTAL_LINES && file.lines.length <= FILE_LINES}
          />
        {/each}
      </div>
    </div>
  {/snippet}
</StateView>
