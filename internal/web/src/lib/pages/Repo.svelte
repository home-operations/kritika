<script lang="ts">
  import Collapsible from '../components/Collapsible.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import { href } from '../router.svelte';
  import { Resource } from '../resource.svelte';
  import { duration, indexTone, shortSha, bytes, wholeNumber } from '../format';
  import type { Condition, ConfigSource, ModelRoute, Page, Pull, RepoDetail, RepoSettings, Risk, SkillScope } from '../types';
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

  const res = Resource.live<RepoDetail>(() => `${account}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`);
  const pulls = Resource.live<Page<Pull>>(() => `${account}/pulls?state=all&limit=50&repo=${encodeURIComponent(fullName)}`);

  $effect(() => {
    void res.load();
  });
  $effect(() => {
    void pulls.load();
  });

  const list = (xs: string[]) => (xs.length ? xs.join(', ') : '—');
  const tests = (c: Condition) => [c.expr, c.paths?.length ? `paths ${c.paths.join(', ')}` : ''].filter(Boolean).join(' && ');
  // Conditions are parted by a semicolon: a condition's globs are already parted by commas.
  const conditions = (cs: Condition[]) => cs.map((c) => (c.name ? `${c.name}: ${tests(c)}` : tests(c))).join('; ') || '—';
  // A route's conditions are parted by "or": one holding picks its model.
  const routes = (rs: ModelRoute[] = []) =>
    rs
      .map((r) => {
        const model = `${r.model}${r.effort ? `/${r.effort}` : ''}${r.fallback ? `, fallback ${r.fallback}` : ''}`;
        const when = r.when.map((c) => (c.name ? `${c.name}: ${tests(c)}` : tests(c))).join(' or ');
        return `${r.name ? `${r.name}: ` : ''}${model} when ${when}`;
      })
      .join('; ') || '—';
  const narrows = (sc: SkillScope) => [sc.paths?.length ? `paths ${sc.paths.join(', ')}` : '', (sc.when ?? []).map((w) => w.expr).join(' or ')].filter(Boolean).join(' && ');
  const scopes = (scope: Record<string, SkillScope>) =>
    Object.entries(scope)
      .map(([name, sc]) => `${name}${sc.load ? ' (loaded)' : ''}: ${narrows(sc)}`)
      .join('; ') || '—';
  const floors = (f: Partial<Record<Risk, string[]>> | undefined) =>
    Object.entries(f ?? {})
      .map(([level, globs]) => `${level}: ${(globs ?? []).join(', ')}`)
      .join('; ') || '—';
  const yes = (b: boolean) => (b ? 'yes' : 'no');
  // bounded is the conditions a setting holds under: the admin's, and the
  // repository file's, which must hold as well; '' for none.
  const bounded = (admin: Condition[] = [], repo: Condition[] = []) =>
    [admin.length ? `when ${conditions(admin)}` : '', repo.length ? `${admin.length ? 'and' : 'when'} ${conditions(repo)} (.kritika.yaml)` : '']
      .filter(Boolean)
      .join(' ');
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
    { label: 'Model routes', key: 'review.model', value: (s) => routes(s.models.routes), mono: true },
    { label: 'Fallback model', key: 'review.fallback', value: (s) => s.models.fallback || '—', mono: true },
    { label: 'Review effort', key: 'review.effort', value: (s) => s.models.effort || "the provider's default" },
    { label: 'Confidence model', key: 'confidence.model', value: (s) => s.confidence.model || '—', mono: true },
    {
      label: 'Confidence when',
      key: 'confidence.when',
      value: (s) => (!s.confidence.model ? '—' : bounded(s.confidence.when, s.confidence.whenInRepo) || 'every pull request'),
      mono: true,
    },
    { label: 'Confidence fallback', key: 'confidence.fallback', value: (s) => (s.confidence.model ? s.confidence.fallback || '—' : '—'), mono: true },
    { label: 'Confidence effort', key: 'confidence.effort', value: (s) => (s.confidence.model ? s.confidence.effort || "the provider's default" : '—') },
    { label: 'Confidence threshold', key: 'confidence.threshold', value: (s) => (s.confidence.model ? `${s.confidence.threshold}/5` : '—') },
    {
      label: 'Confidence gate',
      key: 'confidence.gate',
      value: (s) =>
        !s.confidence.model
          ? '—'
          : s.confidence.gate
            ? `yes${s.confidence.gateWhen?.length ? ` ${bounded(s.confidence.gateWhen)}` : ''}: the check fails under the threshold`
            : 'no: the check reports the score',
    },
    {
      label: 'Approve up to risk',
      key: 'confidence.risk',
      value: (s) => (!s.review.approve ? '—' : s.confidence.model ? s.confidence.risk : 'any: no scorer, so on the findings alone'),
    },
    { label: 'Risk instructions', key: 'confidence.instructions', value: (s) => s.confidence.instructions || '—' },
    { label: 'Risk floors', key: 'confidence.floors', value: (s) => floors(s.confidence.floors), mono: true },
    { label: 'Include', key: 'trigger.include', value: (s) => conditions(s.filters.include), mono: true },
    { label: 'Exclude', key: 'trigger.exclude', value: (s) => conditions(s.filters.exclude), mono: true },
    { label: 'Ignore', key: 'ignore', value: (s) => list(s.ignore), mono: true },
    { label: 'Max automatic reviews', key: 'trigger.limit', value: (s) => unlimited(s.maxAutoReviews) },
    { label: 'Max delta files', key: 'review.incremental', value: (s) => wholeNumber(s.maxDeltaFiles) },
    { label: 'Context files', key: 'context', value: (s) => list(s.review.context.map((c) => c.path)), mono: true },
    { label: 'Skills', key: 'skills.paths', value: (s) => (s.skills.paths.length ? s.skills.paths.join(', ') : 'off'), mono: true },
    { label: 'Skill scopes', key: 'skills.scope', value: (s) => scopes(s.skills.scope), mono: true },
    { label: 'Require suggested fix', key: 'review.fixes', value: (s) => yes(s.review.requireSuggestedFix) },
    { label: 'Inline comments', key: 'comments', value: (s) => yes(s.review.inlineComments) },
    { label: 'Dashboard in comments', key: 'comments.dashboard', value: (s) => yes(s.review.dashboard) },
    {
      label: 'Approve',
      key: 'review.approve',
      value: (s) => (!s.review.approve ? 'no' : bounded(s.review.approveWhen, s.review.approveWhenInRepo) || 'yes'),
    },
    { label: 'Flow diagram', key: 'review.diagram', value: (s) => yes(s.review.diagram) },
    { label: 'Cost in footer', key: 'review.cost', value: (s) => yes(s.review.cost) },
    { label: 'Replies in finding threads', key: 'review.replies', value: (s) => yes(s.review.replies) },
    { label: 'Concurrency', key: 'limits', value: (s) => unlimited(s.limits.concurrency) },
    { label: 'Reviews / day', key: 'limits', value: (s) => unlimited(s.limits.reviewsPerDay) },
    { label: 'Tokens / month', key: 'limits', value: (s) => unlimited(s.limits.tokensPerMonth) },
  ];
  const agentRows: Row[] = [
    { label: 'Max steps', key: 'agent.steps', value: (s) => wholeNumber(s.agent.maxSteps) },
    { label: 'Max tool output', key: 'agent.output', value: (s) => bytes(s.agent.maxToolOutputBytes) },
    { label: 'Max tokens', key: 'agent.tokens', value: (s) => wholeNumber(s.agent.maxTokens) },
    { label: 'Max prompt tokens', key: 'agent.prompt', value: (s) => wholeNumber(s.agent.maxPromptTokens) },
    { label: 'Max parts', key: 'agent.parts', value: (s) => wholeNumber(s.agent.maxParts) },
    { label: 'Timeout', key: 'agent.timeout', value: (s) => duration(s.agent.timeoutSeconds * 1000) },
    { label: 'Command timeout', key: 'agent.commandTimeout', value: (s) => duration(s.agent.commandTimeoutSeconds * 1000) },
    { label: 'Max fetches', key: 'agent.fetches', value: (s) => wholeNumber(s.agent.maxFetches) },
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
      <span class="muted small text-ui-sm">(<span class="mono">{sourceLabel.repository}</span>; the admin's is <span class:mono={r.mono}>{own}</span>)</span>
    {:else}
      <span class="muted small text-ui-sm">({sourceLabel[d.sources[r.key] ?? 'default']})</span>
    {/if}
  </dd>
{/snippet}

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title={fullName} {slug} parents={[{ label: 'Repositories', route: { name: 'repos', slug } }]}>
      {#snippet actions()}
        {#if isAdmin() && res.data?.enabled}
          <ActionButton
            label="Reindex"
            title="Reindex the repository?"
            body={`Rebuild the code index of ${fullName} from scratch at its default branch.`}
            path={reindexPath(slug, fullName)}
            done="Reindex queued"
            ondone={() => res.load()}
          />
        {/if}
      {/snippet}
    </PageHeader>
    {#if res.data?.fork || res.data?.archived || res.data?.visibility === 'public' || res.data?.visibility === 'internal'}<p class="meta-line flex flex-wrap items-center gap-y-1 gap-x-3 text-muted text-ui-sm"><RepoTraits r={res.data} /></p>{/if}
    <section class="panel" aria-labelledby="repo-pulls">
      <header class="panel-head">
        <h2 id="repo-pulls">Pull requests</h2>
        <a class="small text-ui-sm" href={href({ name: 'pulls', slug, filter: { state: 'all', repo: fullName } })}>See all</a>
      </header>
      <StateView res={pulls} retry={() => pulls.load()} isEmpty={(p) => p.items.length === 0} empty="No pull requests seen yet.">
        {#snippet children(p)}
          <PullTable {slug} items={p.items} />
        {/snippet}
      </StateView>
    </section>
    <StateView {res} retry={() => res.load()}>
      {#snippet children(d)}
        {@const rc = d.repoConfig}
        {@const enabled = d.enabled && (rc?.settings.enabled ?? true) ? 'yes' : 'no'}
        {@const why = d.archived ? 'archived on GitHub' : d.turnedOn === false ? 'off in the dashboard' : d.turnedOn === true ? 'turned on in the dashboard' : d.fork && !d.enabled ? 'a fork not turned on' : d.managedBy === 'file' ? 'listed in the configuration file' : 'reported by the forge'}
        {@const rows = shown(d, settingRows)}
        {@const agent = shown(d, agentRows)}
        {#if rc?.ignored}<p class="notice border border-solid border-line bg-surface rounded-md py-2.5 px-4 text-ui-md" role="note">Repository configuration ignored: {rc.ignored}</p>{/if}
        <Collapsible title="Repository settings">
          {#snippet meta()}<span class="text-ui-sm text-muted">Reviews {enabled === 'yes' ? 'enabled' : 'disabled'} · default branch <span class="mono">{d.defaultBranch}</span></span>{/snippet}
          <div class="grid-2 grid grid-cols-[repeat(auto-fit,_minmax(320px,_1fr))] gap-x-6 gap-y-3">
            <section class="min-w-0" aria-labelledby="repo-settings">
              <header class="subpanel-head">
                <h3 id="repo-settings">Effective settings</h3>
                <label class="search-box max-w-50 flex"><span class="sr-only">Filter settings</span><input type="search" aria-label="Filter settings" placeholder="Filter settings" bind:value={settingFilter} /></label>
              </header>
              <dl class="deflist grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-3 mx-4 text-ui-sm">
                {#if matches('Enabled', 'enabled', enabled)}
                  <dt>Enabled</dt><dd>{enabled} <span class="muted small text-ui-sm">({why})</span></dd>
                {/if}
                {#if matches('Default branch', d.defaultBranch)}<dt>Default branch</dt><dd class="mono">{d.defaultBranch}</dd>{/if}
                {#each rows as r (r.label)}
                  {@render setting(d, r)}
                {/each}
              </dl>
              {#if rows.length === 0 && agent.length === 0}<p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No setting matches “{settingFilter.trim()}”.</p>{/if}
            </section>
            <section class="min-w-0" aria-labelledby="repo-agent">
              <header class="subpanel-head"><h3 id="repo-agent">Agent limits</h3></header>
              <dl class="deflist grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-3 mx-4 text-ui-sm">
                {#each agent as r (r.label)}
                  {@render setting(d, r)}
                {/each}
              </dl>
            </section>
          </div>

          <section class="min-w-0 border-t border-solid border-t-line" aria-labelledby="repo-file">
            <header class="subpanel-head"><h3 id="repo-file" class="mono">.kritika.yaml</h3></header>
            {#if !rc}
              <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">No review has read it yet.</p>
            {:else}
              <dl class="deflist grid grid-cols-[minmax(120px,_max-content)_minmax(0,_1fr)] gap-y-1 gap-x-3.5 my-3 mx-4 text-ui-sm">
                <dt>Read at</dt>
                <dd>
                  <span class="mono" title={rc.commit}>{shortSha(rc.commit)}</span>
                  <span class="muted small text-ui-sm">(merge base of <a href={href({ name: 'review', slug, id: rc.reviewId })}>the last review</a>)</span>
                </dd>
                {#if rc.found}
                  <dt>Include</dt><dd class="mono">{conditions(rc.filters.include)} <span class="muted small text-ui-sm">(beside the admin's)</span></dd>
                  <dt>Exclude</dt><dd class="mono">{conditions(rc.filters.exclude)} <span class="muted small text-ui-sm">(beside the admin's)</span></dd>
                {/if}
              </dl>
              {#if !rc.found}
                <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">There was no .kritika.yaml at that commit.</p>
              {/if}
              {#if rc.dropped.length}
                <h3 class="subhead mt-3 mx-4 mb-1 text-ui-sm font-[650] text-muted">Settings controlled by the administrator</h3>
                <ul class="plain-list m-0 pl-4">
                  {#each rc.dropped as note (note)}<li class="small text-ui-sm">{note}</li>{/each}
                </ul>
              {/if}
            {/if}
          </section>

        </Collapsible>

        <Collapsible title="Index runs" open={d.indexRuns[0]?.status === 'failed'}>
          {#snippet meta()}{#if d.indexRuns[0]}<span class="text-ui-sm text-muted">latest {d.indexRuns[0].status} <Time iso={d.indexRuns[0].createdAt} /></span>{/if}{/snippet}
          <section id="repo-index" class="min-w-0" aria-label="Index runs">
            {#if d.indexRuns.length === 0}
              <p class="state-msg flex items-center gap-2 m-0 py-3 px-4 text-muted">Never indexed.</p>
            {:else}
              <div class="table-wrap overflow-x-auto max-w-full">
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
                        <td class="mono small text-ui-sm" title={run.commitSha}>{shortSha(run.commitSha)}{run.baseSha ? ` ← ${shortSha(run.baseSha)}` : ''}</td>
                        <td>{run.trigger}</td>
                        <td class="num">{wholeNumber(run.chunkCount)}</td>
                        <td class="mono small text-ui-sm">{run.embedModel}</td>
                        <td><Time iso={run.createdAt} /></td>
                        <td>{run.finishedAt ? duration(Date.parse(run.finishedAt) - Date.parse(run.createdAt)) : '…'}</td>
                        <td class="error-cell text-danger text-ui-sm max-w-80 [word-break:break-word]">{run.error}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </section>
        </Collapsible>
      {/snippet}
    </StateView>
  </div>
</main>
