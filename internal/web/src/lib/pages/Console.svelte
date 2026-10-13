<script lang="ts">
  import HelpPopover from '../components/HelpPopover.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import { href } from '../router.svelte';
  import { Resource } from '../resource.svelte';
  import { tokens, costTotal, wholeNumber } from '../format';
  import type { InstanceSetting, AdminAccount } from '../types';
  import StateView from '../components/StateView.svelte';
  import Pill from '../components/Pill.svelte';
  import AuditTable from '../components/AuditTable.svelte';
  import Checklist from './admin/Checklist.svelte';
  import ConnectionsSection from './admin/ConnectionsSection.svelte';

  const res = Resource.json<AdminAccount[]>(() => '/api/v1/admin/accounts');
  const instance = Resource.json<InstanceSetting[]>(() => '/api/v1/admin/instance');
  $effect(() => {
    void res.load();
  });
  $effect(() => {
    void instance.load();
  });
  let search = $state('');
  let section = $state('');
  const sections = $derived([...new Set(instance.data?.map((row) => row.section) ?? [])]);
  const filtered = $derived((instance.data ?? []).filter((row) =>
    (!section || row.section === section) &&
    [row.key, row.value, row.section, sourceLabel[row.source] ?? row.source].some((value) => value.toLowerCase().includes(search.trim().toLowerCase()))));
  const sourceLabel: Record<string, string> = { env: 'environment', file: 'config file', default: 'default' };
</script>

<svelte:head><title>Instance settings · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader title="Instance settings" description="Manage connections and inspect instance configuration." />
    {#if res.data}<Checklist accounts={res.data} />{/if}
    <section class="panel" aria-labelledby="op-accounts">
      <header class="panel-head"><h2 id="op-accounts">Accounts</h2><HelpPopover label="About accounts"><p>Accounts served by a GitHub App, including configured accounts that are not yet connected.</p></HelpPopover></header>
      <StateView {res} retry={() => res.load()} isEmpty={(d) => d.length === 0} empty="No accounts yet: declare a GitHub App under apps in the configuration file.">
        {#snippet children(list)}
          <div class="table-wrap overflow-x-auto max-w-full">
            <table class="data">
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">App</th>
                  <th scope="col">State</th>
                  <th scope="col" class="num">Repos</th>
                  <th scope="col" class="num">Reviews 7d</th>
                  <th scope="col" class="num">Tokens (month)</th>
                  <th scope="col" class="num wrap">API spend (month)</th>
                </tr>
              </thead>
              <tbody>
                {#each list as t (t.slug)}
                  <tr>
                    <td class="mono name-fill" title={t.slug}>
                      {#if t.live}<a href={href({ name: 'account', slug: t.slug })}>{t.slug}</a>{:else}{t.slug}{/if}
                    </td>
                    <td class="mono small name-clip text-ui-sm" title={t.connection}>{t.connection || '—'}</td>
                    <td>
                      {#if t.conflict}
                        <Pill tone="warn" label="not served" />
                        <span class="small muted text-ui-sm">{t.conflict}</span>
                      {:else}
                        <Pill tone="ok" label="live" />
                      {/if}
                    </td>
                    <td class="num">{wholeNumber(t.repositories)}</td>
                    <td class="num">{wholeNumber(t.reviews7d)}</td>
                    <td class="num">{tokens(t.usage.tokens)}</td>
                    <td class="num">{costTotal(t.usage.costUsd, t.usage.unpricedCalls)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/snippet}
      </StateView>
    </section>

    <ConnectionsSection />

    <section class="panel" aria-labelledby="op-instance">
      <header class="panel-head"><h2 id="op-instance">Instance configuration</h2><HelpPopover label="About instance configuration"><p>Read-only values from this process's environment and configuration file. Source shows where each value is set.</p></HelpPopover></header>
      <StateView res={instance} retry={() => instance.load()} isEmpty={(d) => d.length === 0} empty="No instance settings.">
        <div class="toolbar flex flex-wrap items-center gap-2 p-3">
          <input type="search" aria-label="Search configuration" placeholder="Search configuration" bind:value={search} />
          <select aria-label="Configuration section" bind:value={section}>
            <option value="">All sections</option>
            {#each sections as name (name)}<option value={name}>{name}</option>{/each}
          </select>
          {#if search || section}<button class="btn" onclick={() => { search = ''; section = ''; }}>Clear filters</button>{/if}
        </div>
        <div class="table-wrap overflow-x-auto max-w-full">
          <table class="data">
            <thead>
              <tr><th scope="col">Section</th><th scope="col">Setting</th><th scope="col">Value</th><th scope="col">Source</th></tr>
            </thead>
            <tbody>
              {#each filtered as row (`${row.section}:${row.key}`)}
                <tr>
                  <td>{row.section}</td>
                  <td class="mono">{row.key}</td>
                  <td class="mono">{row.value}</td>
                  <td>{sourceLabel[row.source] ?? row.source}</td>
                </tr>
              {:else}
                <tr><td colspan="4" class="muted">No settings match these filters.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </StateView>
    </section>

    <section class="panel" aria-labelledby="op-audit">
      <header class="panel-head"><h2 id="op-audit">Admin audit log</h2></header>
      <AuditTable path="/api/v1/admin/audit" showAccount />
    </section>
  </div>
</main>
