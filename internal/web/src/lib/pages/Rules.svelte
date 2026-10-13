<script lang="ts">
  import PageHeader from '../components/PageHeader.svelte';
  import HelpPopover from '../components/HelpPopover.svelte';
  // What the account's reviews check: rules written in the configuration,
  // as text or a file, and context files that explain the code, with where
  // each is set and the repositories that read it. Read-only: rules are set
  // in the configuration or a repository's .kritika.yaml.
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { Resource } from '../resource.svelte';
  import { accountApi, repoRoute } from '../links';
  import { findingFilter } from '../routes';
  import { wholeNumber } from '../format';
  import { parseTokens, type TokenSpec } from '../tokensearch';
  import type { Rule, RuleKind, RuleSource } from '../types';
  import StateView from '../components/StateView.svelte';
  import TokenSearch from '../components/TokenSearch.svelte';
  import Icon from '../Icon.svelte';
  import { mdiCheckDecagramOutline, mdiFileDocumentOutline } from '../icons';

  let { slug }: { slug: string } = $props();

  const res = new Resource(() => getJSON<Rule[]>(`${accountApi(slug)}/rules`));
  $effect(() => {
    void res.load();
  });

  const KINDS: Record<RuleKind, { label: string; icon: string; what: string; order: number }> = {
    rule: { label: 'Rule', icon: mdiCheckDecagramOutline, what: 'a check written in the configuration', order: 0 },
    context: { label: 'Context', icon: mdiFileDocumentOutline, what: 'a file that explains the code', order: 1 },
  };
  const SOURCES: Record<RuleSource, string> = {
    default: "kritika's default",
    env: 'Environment',
    file: 'Config file',
    defaults: 'Instance settings',
    account: 'Account entry',
    entry: 'Repository entry',
    repository: '.kritika.yaml',
  };

  const repoNames = $derived([...new Set((res.data ?? []).flatMap((r) => r.repositories))].sort());
  const specs = $derived<TokenSpec[]>([
    { key: 'repo', hint: 'a repository', values: repoNames },
    { key: 'kind', hint: 'rule or context', values: ['rule', 'context'] },
    { key: 'source', hint: 'where it is set', values: ['default', 'env', 'file', 'defaults', 'account', 'entry', 'repository'] },
  ]);
  let text = $state('');
  let applied = $state(parseTokens('', []));

  // citedBy is the findings list narrowed to a written rule's citations,
  // and to its repository when it has one alone.
  const citedBy = (r: Rule) => findingFilter({ rule: r.id, repo: r.repositories.length === 1 ? r.repositories[0] : undefined });

  function shown(rules: Rule[]): Rule[] {
    const { tokens, q } = applied;
    const needle = q?.toLowerCase();
    const has = (s: string) => !!needle && s.toLowerCase().includes(needle);
    return rules
      .filter(
        (r) =>
          (!tokens.repo || r.repositories.includes(tokens.repo)) &&
          (!tokens.kind || r.kind === tokens.kind) &&
          (!tokens.source || r.source === tokens.source) &&
          (!needle || has(r.path) || has(r.description) || has(r.id) || has(r.text) || r.when.some((w) => has(w.name) || has(w.expr))),
      )
      .sort((a, b) => KINDS[a.kind].order - KINDS[b.kind].order);
  }
</script>

<svelte:head><title>Rules · {slug} · kritika</title></svelte:head>

<main class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Rules" {slug}>
      {#snippet actions()}
        <HelpPopover label="About rules">
          <p>
            What reviews check: rules written in the configuration, as text or a file, and context files that explain the code.
            Rules are written, and files named, under <span class="mono">rules</span> and
            <span class="mono">context</span> in the configuration file or a repository's own
            <span class="mono">.kritika.yaml</span>. To offer <span class="mono">AGENTS.md</span> or
            <span class="mono">CLAUDE.md</span> as reference material, list it under <span class="mono">context</span>.
          </p>
        </HelpPopover>
      {/snippet}
    </PageHeader>
    <div class="toolbar flex flex-wrap items-center gap-2" role="search">
      <TokenSearch
        id="rule-search"
        label="Search rules"
        placeholder="Search, or filter by repo:, kind: or source:"
        {specs}
        bind:text
        ready={!!res.data}
        onapply={(p) => (applied = p)}
      />
    </div>
    <StateView
      {res}
      retry={() => res.load()}
      isEmpty={(d) => d.length === 0}
      empty="No rules yet: write them, or name files for them, under rules, and name context files under context, in the configuration or a repository's .kritika.yaml."
    >
      {#snippet children(rules)}
        {@const rows = shown(rules)}
        {#if rows.length === 0}
          <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">No rule matches.</p>
        {:else}
          <div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
            <table class="data rule-table">
              <thead>
                <tr>
                  <th scope="col">Rule</th>
                  <th scope="col">Applies to</th>
                  <th scope="col">Set in</th>
                  <th scope="col">Repositories</th>
                  <th scope="col" class="num" title="Findings that cite a rule, and how many a later review found addressed">Findings</th>
                </tr>
              </thead>
              <tbody>
                {#each rows as r (`${r.kind}\u0000${r.id}\u0000${r.path}\u0000${r.source}\u0000${r.description}\u0000${r.text}\u0000${r.paths.join()}\u0000${r.when.map((w) => `${w.name}\u0001${w.expr}`).join()}\u0000${r.repositories.join()}`)}
                  {@const k = KINDS[r.kind]}
                  <tr>
                    <td class="wrap">
                      <div class="rule-main flex items-start gap-2 min-w-60">
                        <span class="rule-kind inline-flex items-center justify-center flex-none w-5.5 h-5.5 rounded-sm text-ink bg-surface border border-solid border-line" title="{k.label}: {k.what}"><Icon path={k.icon} size={14} label={k.label} /></span>
                        {#if r.kind === 'rule' && r.path}
                          <span class="rule-text flex flex-col gap-0.5 min-w-0">
                            <span class="mono rule-path text-ui-md font-semibold wrap-anywhere">{r.path}</span>
                            <span class="mono rule-sub text-ui-sm text-muted">{r.id}</span>
                          </span>
                        {:else if r.kind === 'rule'}
                          <span class="rule-text flex flex-col gap-0.5 min-w-0">
                            <span class="rule-body [display:-webkit-box] [-webkit-box-orient:vertical] [-webkit-line-clamp:3] [line-clamp:3] overflow-hidden text-ui-md whitespace-pre-line" title={r.text}>{r.text}</span>
                            <span class="mono rule-sub text-ui-sm text-muted">{r.id}</span>
                          </span>
                        {:else}
                          <span class="rule-text flex flex-col gap-0.5 min-w-0">
                            <span class="mono rule-path text-ui-md font-semibold wrap-anywhere">{r.path}</span>
                            {#if r.description}<span class="rule-sub text-ui-sm text-muted">{r.description}</span>{/if}
                          </span>
                        {/if}
                      </div>
                    </td>
                    <td>
                      {#if r.paths.length}
                        <span class="rule-globs inline-flex flex-wrap gap-1">{#each r.paths as p (p)}<code>{p}</code>{/each}</span>
                      {:else if !r.when.length}<span class="muted small text-ui-sm">every change</span>{/if}
                      {#each r.when as w, i (i)}
                        <span class="rule-when block mt-0.5 break-all" title="Applies only to a pull request one of these is true of"
                          ><span class="small muted text-ui-sm">{i ? 'or' : 'when'}{w.name ? ` ${w.name}:` : ''}</span> <code>{w.expr}</code></span
                        >
                      {/each}
                    </td>
                    <td class="small text-ui-sm" class:mono={r.source === 'repository'}>{SOURCES[r.source] ?? r.source}</td>
                    <td class="wrap" title={r.repositories.join('\n')}>
                      <div class="rule-repos flex flex-wrap items-baseline gap-y-0.5 gap-x-2">
                        {#each r.repositories.slice(0, 2) as name (name)}
                          <a class="mono small text-ui-sm" href={href(repoRoute(slug, name))}>{name}</a>
                        {/each}
                        {#if r.repositories.length > 2}<span class="small muted text-ui-sm">and {r.repositories.length - 2} more</span>{/if}
                      </div>
                    </td>
                    <td class="num">
                      {#if r.kind === 'rule'}
                        <span class="rule-cited inline-flex flex-col items-end">
                          {#if r.findings}
                            <a href={href({ name: 'findings', slug, filter: citedBy(r) })} title="Findings that cite {r.id}">{wholeNumber(r.findings)}</a>
                            <span class="small muted text-ui-sm">{wholeNumber(r.addressed)} addressed</span>
                          {:else}<span class="muted">0</span>{/if}
                        </span>
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      {/snippet}
    </StateView>
  </div>
</main>
