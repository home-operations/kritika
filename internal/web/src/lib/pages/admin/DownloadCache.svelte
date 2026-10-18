<script lang="ts">
  import type { InstanceSetting } from '../../types';
  import { bytes } from '../../format';
  import Pill from '../../components/Pill.svelte';

  let { settings }: { settings: InstanceSetting[] } = $props();
  function value(file: string, env: string): string | undefined {
    return settings.find((row) =>
      (row.section === 'file' && row.key === file) ||
      (row.section === 'environment' && row.key === `KRITIKA_GATEWAY_CACHE_${env}`))?.value;
  }
  const dir = $derived(value('gatewayCacheDir', 'DIR'));
  const enabled = $derived(dir !== undefined && dir !== '');
  const backend = $derived(value('gatewayCacheStorage', 'STORAGE'));
  const entrySize = $derived(value('gatewayCacheMaxEntrySize', 'MAX_ENTRY_SIZE'));
  function size(raw: string | undefined): string {
    if (raw === undefined) return 'Unavailable';
    return /^\d+$/.test(raw) ? bytes(Number(raw)) : raw;
  }
</script>

<section class="panel" aria-labelledby="op-download-cache">
  <header class="panel-head"><h2 id="op-download-cache">Download cache</h2><Pill label={dir === undefined ? 'Unavailable' : enabled ? 'Enabled' : 'Disabled'} /></header>
  <div class="flex flex-col gap-3 px-4 py-3">
    {#if dir === undefined}
      <p class="small muted text-ui-sm">Cache configuration is unavailable from this replica.</p>
    {:else if enabled}
      <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-ui-sm">
        <dt>Storage</dt><dd>{backend === 's3' ? 'S3 · shared across replicas and rollouts' : backend === 'filesystem' ? 'Filesystem · private to each replica' : 'Unavailable'}</dd>
        {#if backend === 's3'}
          <dt>Bucket</dt><dd class="mono break-all">{value('gatewayCacheS3Bucket', 'S3_BUCKET') ?? 'Unavailable'}</dd>
          <dt>Prefix</dt><dd class="mono break-all">{value('gatewayCacheS3Prefix', 'S3_PREFIX') || '(bucket root)'}</dd>
        {/if}
        <dt>Maximum download size</dt><dd>{size(entrySize)}</dd>
        {#if backend === 'filesystem'}
          <dt>Local entry budget</dt><dd>{size(value('gatewayCacheMaxSize', 'MAX_SIZE'))}</dd>
        {/if}
      </dl>
      {#if backend === 's3'}
        <p class="small muted text-ui-sm">Bucket retention is managed externally. Refresh after bounds how long a download is reused before it is fetched again; it deletes nothing.</p>
      {/if}
      <p class="small muted text-ui-sm">Configuration from the responding replica. Storage availability is not tested here; use the Grafana Download cache panels for traffic and errors.</p>
    {:else}
      <p class="small muted text-ui-sm">Downloads are forwarded without being stored in the gateway cache.</p>
    {/if}
  </div>
</section>
