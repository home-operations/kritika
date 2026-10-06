<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { Resource, live } from '../resource.svelte';
  import { duration, indexTone, shortSha, bytes, wholeNumber } from '../format';
  import type { Condition, ConfigSource, Page, Pull, RepoDetail, RepoSettings, SkillScope } from '../types';
  import StateView from '../components/StateView.svelte';
  import Pill from '../components/Pill.svelte';
  import Time from '../components/Time.svelte';
  import PullTable from '../components/PullTable.svelte';
  import ActionButton from '../components/ActionButton.svelte';
  import RepoTraits from '../components/RepoTraits.svelte';
  import { reindexPath, accountApi } from '../links';
  import { isAdmin } from '../session.svelte';

  let { slug, owner, repo }: { slug: string; owner: string; repo: string } = $props();
  const fullName = $derived(`${owner}/${repo}`);
  const account = $derived(`${accountApi(slug)}`);

  const res = new Resource(() =>
    getJSON<RepoDetail>(`${account}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`),
  );
  const pulls = new Resource(() =>
    getJSON<Page<Pull>>(
      `${account}/pulls?state=all&limit=50&repo=${encodeURIComponent(fullName)}`,
    ),
  );

  $effect(() => {
    void res.load();
  });
  $effect(() => {
    void pulls.load();
  });
  $effect(() =>
    live(
      (e) => e.account === slug && e.kind !== 'model_call',
      () => {
        void res.load();
        void pulls.load();
      },
    ),
  );

  const list = (xs: string[]) => (xs.length ? xs.join(', ') : '—');
  const tests = (c: Condition) => [c.expr, c.paths?.length ? `paths ${c.paths.join(', ')}` : ''].filter(Boolean).join(' && ');
  // Conditions are parted by a semicolon: a condition's globs are already parted by commas.
  const conditions = (cs: Condition[]) => cs.map((c) => (c.name ? `${c.name}: ${tests(c)}` : tests(c))).join('; ') || '—';
  const narrows = (sc: SkillScope) => [sc.paths?.length ? `paths ${sc.paths.join(', ')}` : '', (sc.when ?? []).map((w) => w.expr).join(' or ')].filter(Boolean).join(' && ');
  const scopes = (scope: Record<string, SkillScope>) => Object.entries(scope).map(([name, sc]) => `${name}: ${narrows(sc)}`).join('; ') || '—';
  const yes = (b: boolean) => (b ? 'yes' : 'no');
  const unlimited = (n: number) => (n ? wholeNumber(n) : 'unlimited');

  // One setting as the page shows it: its label, the policy key its source
  // is reported under, and how to show its value.
  interface Row {
    label: string;
    key: string;
    value: (s: RepoSettings) => string;
    mono?: boolean;
  }
  const settingRows: Row[] = [
    { label: 'Review model', key: 'review.model', value: (s) => s.models.review || '—', mono: true },
    { label: 'Fallback model', key: 'review.fallback', value: (s) => s.models.fallback || '—', mono: true },
    { label: 'Confidence model', key: 'confidence.model', value: (s) => s.confidence.model || '—', mono: true },
    { label: 'Confidence threshold', key: 'confidence.threshold', value: (s) => (s.confidence.model ? `${s.confidence.threshold}/5` : '—') },
    {
      label: 'Confidence gate',
      key: 'confidence.gate',
      value: (s) => (!s.confidence.model ? '—' : s.confidence.gate ? 'yes: the check fails under the threshold' : 'no: the check reports the score'),
    },
    {
      label: 'Approve up to risk',
      key: 'confidence.risk',
      value: (s) => (!s.review.approve ? '—' : s.confidence.model ? s.confidence.risk : 'any: no scorer, so on the findings alone'),
    },
    { label: 'Risk instructions', key: 'confidence.instructions', value: (s) => s.confidence.instructions || '—' },
    { label: 'Include', key: 'trigger.include', value: (s) => conditions(s.filters.include), mono: true },
    { label: 'Exclude', key: 'trigger.exclude', value: (s) => conditions(s.filters.exclude), mono: true },
    { label: 'Ignore', key: 'ignore', value: (s) => list(s.ignore), mono: true },
    { label: 'Settle', key: 'trigger.settle', value: (s) => duration(s.settleSeconds * 1000) || '0s' },
    { label: 'Max automatic reviews', key: 'trigger.limit', value: (s) => unlimited(s.maxAutoReviews) },
    { label: 'Max delta files', key: 'review.incremental', value: (s) => wholeNumber(s.maxDeltaFiles) },
    { label: 'Context files', key: 'context', value: (s) => list(s.review.context.map((c) => c.path)), mono: true },
    { label: 'Skills', key: 'skills.paths', value: (s) => (s.skills.paths.length ? s.skills.paths.join(', ') : 'off'), mono: true },
    { label: 'Skill scopes', key: 'skills.scope', value: (s) => scopes(s.skills.scope), mono: true },
    { label: 'Require suggested fix', key: 'review.fixes', value: (s) => yes(s.review.requireSuggestedFix) },
    { label: 'Inline comments', key: 'comments', value: (s) => yes(s.review.inlineComments) },
    { label: 'Approve', key: 'review.approve', value: (s) => yes(s.review.approve) },
    { label: 'Flow diagram', key: 'review.diagram', value: (s) => yes(s.review.diagram) },
    { label: 'Cost in footer', key: 'review.cost', value: (s) => yes(s.review.cost) },
    { label: 'Feedback', key: 'review.feedback', value: (s) => s.review.feedback },
    { label: 'Concurrency', key: 'limits', value: (s) => unlimited(s.limits.concurrency) },
    { label: 'Reviews / day', key: 'limits', value: (s) => unlimited(s.limits.reviewsPerDay) },
    { label: 'Tokens / month', key: 'limits', value: (s) => unlimited(s.limits.tokensPerMonth) },
  ];
  const agentRows: Row[] = [
    { label: 'Max steps', key: 'agent.steps', value: (s) => wholeNumber(s.agent.maxSteps) },
    { label: 'Max tool output', key: 'agent.output', value: (s) => bytes(s.agent.maxToolOutputBytes) },
    { label: 'Max tokens', key: 'agent.tokens', value: (s) => wholeNumber(s.agent.maxTokens) },
    { label: 'Max prompt tokens', key: 'agent.prompt', value: (s) => wholeNumber(s.agent.maxPromptTokens) },
    { label: 'Timeout', key: 'agent.timeout', value: (s) => duration(s.agent.timeoutSeconds * 1000) },
    { label: 'Commands', key: 'agent.commands', value: (s) => list(s.agent.commands), mono: true },
    { label: 'Command timeout', key: 'agent.commandTimeout', value: (s) => duration(s.agent.commandTimeoutSeconds * 1000) },
  ];
  const sourceLabel: Record<ConfigSource, string> = {
    default: 'default',
    env: 'environment',
    file: 'config file',
    defaults: 'instance',
    account: 'account',
    repository: '.kritika.yaml',
  };

  // settingFilter narrows the settings shown to those whose label, key or
  // value holds it.
  let settingFilter = $state('');
  function matches(...texts: string[]): boolean {
    const needle = settingFilter.trim().toLowerCase();
    return !needle || texts.some((t) => t.toLowerCase().includes(needle));
  }
  function shown(d: RepoDetail, rows: Row[]): Row[] {
    const eff = d.repoConfig?.settings ?? d.settings;
    return rows.filter((r) => matches(r.label, r.key, r.value(eff)));
  }
</script>

<svelte:head><title>{fullName} · kritika</title></svelte:head>

{#snippet setting(d: RepoDetail, r: Row)}
  {@const eff = d.repoConfig?.settings ?? d.settings}
  {@const value = r.value(eff)}
  {@const own = r.value(d.settings)}
  <dt>{r.label}</dt>
  <dd>
    <span class:mono={r.mono}>{value}</span>
    {#if value !== own}
      <span class="muted small">(<span class="mono">{sourceLabel.repository}</span>; the admin's is <span class:mono={r.mono}>{own}</span>)</span>
    {:else}
      <span class="muted small">({sourceLabel[d.sources[r.key] ?? 'default']})</span>
    {/if}
  </dd>
{/snippet}

<main class="page">
  <div class="page-inner">
    <header class="page-head">
      <p class="crumbs"><a href={href({ name: 'repos', slug })}>Repositories</a> /</p>
      <h1 class="mono">{fullName}</h1>
      {#if res.data?.fork || res.data?.archived}<p class="meta-line"><RepoTraits r={res.data} /></p>{/if}
      <!-- A repository that is off is not indexed. -->
      {#if isAdmin() && res.data?.enabled}
        <div class="page-actions">
          <ActionButton
            label="Reindex"
            title="Reindex the repository?"
            body={`Rebuild the code index of ${fullName} from scratch at its default branch.`}
            path={reindexPath(slug, fullName)}
            done="Reindex queued"
            ondone={() => res.load()}
          />
        </div>
      {/if}
    </header>
    <StateView {res} retry={() => res.load()}>
      {#snippet children(d)}
        {@const rc = d.repoConfig}
        {@const enabled = d.enabled && (rc?.settings.enabled ?? true) ? 'yes' : 'no'}
        {@const why = d.archived ? 'archived on GitHub' : d.fork && !d.enabled ? 'a fork not turned on' : d.managedBy === 'file' ? 'listed in the configuration file' : 'reported by the forge'}
        {@const rows = shown(d, settingRows)}
        {@const agent = shown(d, agentRows)}
        <div class="grid-2">
          <section class="panel" aria-labelledby="repo-settings">
            <header class="panel-head">
              <h2 id="repo-settings">Effective settings</h2>
              <input class="settings-filter" type="search" aria-label="Filter settings" placeholder="Filter settings" bind:value={settingFilter} />
            </header>
            <dl class="deflist">
              {#if matches('Enabled', 'enabled', enabled)}
                <dt>Enabled</dt><dd>{enabled} <span class="muted small">({why})</span></dd>
              {/if}
              {#if matches('Default branch', d.defaultBranch)}<dt>Default branch</dt><dd class="mono">{d.defaultBranch}</dd>{/if}
              {#each rows as r (r.label)}
                {@render setting(d, r)}
              {/each}
            </dl>
            {#if rows.length === 0 && agent.length === 0}<p class="state-msg">No setting matches “{settingFilter.trim()}”.</p>{/if}
          </section>
          <section class="panel" aria-labelledby="repo-agent">
            <header class="panel-head"><h2 id="repo-agent">Agent limits</h2></header>
            <dl class="deflist">
              {#each agent as r (r.label)}
                {@render setting(d, r)}
              {/each}
            </dl>
          </section>
        </div>

        <section class="panel" aria-labelledby="repo-file">
          <header class="panel-head"><h2 id="repo-file" class="mono">.kritika.yaml</h2></header>
          {#if !rc}
            <p class="state-msg">No review has read it yet.</p>
          {:else}
            <dl class="deflist">
              <dt>Read at</dt>
              <dd>
                <span class="mono" title={rc.commit}>{shortSha(rc.commit)}</span>
                <span class="muted small">(merge base of <a href={href({ name: 'review', slug, id: rc.reviewId })}>the last review</a>)</span>
              </dd>
              {#if rc.found}
                <dt>Include</dt><dd class="mono">{conditions(rc.filters.include)} <span class="muted small">(beside the admin's)</span></dd>
                <dt>Exclude</dt><dd class="mono">{conditions(rc.filters.exclude)} <span class="muted small">(beside the admin's)</span></dd>
              {/if}
            </dl>
            {#if !rc.found}
              <p class="state-msg">There was no .kritika.yaml at that commit.</p>
            {/if}
            {#if rc.ignored}
              <p class="notice" role="note">Ignored as a whole: {rc.ignored}</p>
            {/if}
            {#if rc.dropped.length}
              <h3 class="subhead">Values the file may not set, where the admin's apply instead</h3>
              <ul class="plain-list">
                {#each rc.dropped as note (note)}<li class="small">{note}</li>{/each}
              </ul>
            {/if}
          {/if}
        </section>

        <section class="panel" aria-labelledby="repo-index">
          <header class="panel-head"><h2 id="repo-index">Index runs</h2></header>
          {#if d.indexRuns.length === 0}
            <p class="state-msg">Never indexed.</p>
          {:else}
            <div class="table-wrap">
              <table class="data">
                <thead>
                  <tr>
                    <th scope="col">Status</th><th scope="col">Mode</th><th scope="col">Commit</th><th scope="col">Trigger</th>
                    <th scope="col" class="num">Chunks</th><th scope="col">Embed model</th><th scope="col">Started</th>
                    <th scope="col">Took</th><th scope="col">Error</th>
                  </tr>
                </thead>
                <tbody>
                  {#each d.indexRuns as run (run.id)}
                    <tr>
                      <td><Pill tone={indexTone[run.status]} label={run.status} /></td>
                      <td>{run.mode}</td>
                      <td class="mono small" title={run.commitSha}>{shortSha(run.commitSha)}{run.baseSha ? ` ← ${shortSha(run.baseSha)}` : ''}</td>
                      <td>{run.trigger}</td>
                      <td class="num">{wholeNumber(run.chunkCount)}</td>
                      <td class="mono small">{run.embedModel}</td>
                      <td><Time iso={run.createdAt} /></td>
                      <td>{run.finishedAt ? duration(Date.parse(run.finishedAt) - Date.parse(run.createdAt)) : '…'}</td>
                      <td class="error-cell">{run.error}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </section>
      {/snippet}
    </StateView>

    <section class="panel" aria-labelledby="repo-pulls">
      <header class="panel-head">
        <h2 id="repo-pulls">Pull requests</h2>
        <a class="small" href={href({ name: 'pulls', slug, filter: { state: 'all', repo: fullName } })}>See all</a>
      </header>
      <StateView res={pulls} retry={() => pulls.load()} isEmpty={(p) => p.items.length === 0} empty="No pull requests seen yet.">
        {#snippet children(p)}
          <PullTable {slug} items={p.items} />
        {/snippet}
      </StateView>
    </section>
  </div>
</main>
