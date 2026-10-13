<script lang="ts">
  // The audit log, newest first, one keyset page at a time.
  import { Paged } from '../resource.svelte';
  import type { AuditEvent } from '../types';
  import StateView from './StateView.svelte';
  import LoadMore from './LoadMore.svelte';
  import Time from './Time.svelte';
  import RowToggle from './RowToggle.svelte';

  let { path, showAccount = false }: { path: string; showAccount?: boolean } = $props();

  const paged = new Paged<AuditEvent>(
    (cursor) => (cursor ? `${path}?cursor=${encodeURIComponent(cursor)}` : path),
    (e) => e.id,
  );
  $effect(() => {
    void paged.load();
  });

  const hasDetail = (e: AuditEvent) => Object.keys(e.detail ?? {}).length > 0;
  const tableId = $props.id();
  let expanded = $state<Record<string, boolean>>({});
</script>

<StateView res={paged.first} retry={() => paged.load()} isEmpty={(p) => p.items.length === 0} empty="No audit events yet.">
  <div class="table-wrap overflow-x-auto max-w-full">
    <table class="data audit">
      <thead>
        <tr>
          <th scope="col">Time</th>
          {#if showAccount}<th scope="col">Account</th>{/if}
          <th scope="col">Actor</th>
          <th scope="col">Action</th>
          <th scope="col">Target</th>
          <th scope="col" class="relative"><span class="sr-only">Detail</span></th>
        </tr>
      </thead>
      <tbody>
        {#each paged.items as e (e.id)}
          {@const previewId = `${tableId}-${e.id}`}
          <tr class:row-expanded={expanded[e.id]}>
            <td><Time iso={e.at} /></td>
            {#if showAccount}<td class="mono">{e.account || '—'}</td>{/if}
            <td>{e.actor?.displayName ?? 'system'}</td>
            <td class="mono">{e.action}</td>
            <td class="mono">{e.target}</td>
            <td class="row-toggle-cell">
              {#if hasDetail(e)}<RowToggle bind:open={expanded[e.id]} label={`Detail of ${e.action} on ${e.target}`} controls={previewId} />{/if}
            </td>
          </tr>
          {#if expanded[e.id]}
            <tr class="row-preview"><td colspan={5 + Number(showAccount)}>
              <div class="row-preview-body" id={previewId} role="region" aria-label={`Detail of ${e.action} on ${e.target}`}>
                <pre class="detail-json m-0 font-code text-ui-xs whitespace-pre-wrap [word-break:break-word]">{JSON.stringify(e.detail, null, 2)}</pre>
              </div>
            </td></tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
  <LoadMore {paged} />
</StateView>
