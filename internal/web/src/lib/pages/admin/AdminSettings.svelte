<script lang="ts">
  // The instance's effective configuration, read-only, with the download
  // cache it sets up and the tools recent runs used.
  import HelpPopover from '../../components/HelpPopover.svelte';
  import { Resource } from '../../resource.svelte';
  import type { InstanceSetting } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import DownloadCache from './DownloadCache.svelte';
  import ToolUsage from './ToolUsage.svelte';

  const instance = Resource.json<InstanceSetting[]>(() => '/api/v1/admin/instance');
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

{#if instance.data}<DownloadCache settings={instance.data} />{/if}

<ToolUsage />

<section class="panel" aria-labelledby="op-instance">
  <header class="panel-head"><h2 id="op-instance">Instance configuration</h2><HelpPopover label="About instance configuration"><p>Read-only values from this process's environment and configuration file. Source shows where each value is set.</p></HelpPopover></header>
  <StateView res={instance} retry={() => instance.load()} isEmpty={(d) => d.length === 0} empty="No instance settings.">
    <div class="toolbar flex flex-wrap items-center gap-2 py-3 px-4">
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
