<script lang="ts">
  import { getJSON } from '../../api.svelte';
  import { Resource, live } from '../../resource.svelte';
  import { wholeNumber } from '../../format';
  import type { ToolUsage } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import HelpPopover from '../../components/HelpPopover.svelte';
  import Segmented from '../../components/Segmented.svelte';

  const periods = [
    { value: '7', label: '7 days' },
    { value: '30', label: '30 days' },
    { value: '90', label: '90 days' },
  ] as const;
  let days = $state('30');
  const res = new Resource((signal) => {
    const from = new Date(Date.now() - Number(days) * 86_400_000).toISOString();
    return getJSON<ToolUsage>(`/api/v1/admin/tools?${new URLSearchParams({ from })}`, signal);
  }, () => days);
  $effect(() => { void res.load(); });
  $effect(() => live((e) => e.kind === 'review', () => void res.load(), 1000));
</script>

<section class="panel" aria-labelledby="op-tools">
  <header class="panel-head">
    <h2 id="op-tools">Most used tools</h2>
    <HelpPopover label="About tool usage">
      <p>Top 10 command names across served accounts, combining versions and providers. Every process started counts, including nonzero exits, timeouts and retry runs. Rejected commands and failed installations do not count.</p>
      <p>Counts appear when a run saves its result, using that save time for the period. Active runs and runs that never saved a result are not included.</p>
    </HelpPopover>
  </header>
  <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
    <Segmented label="Tool usage period" options={periods} value={days} onchange={(value) => { days = value; }} />
    <button class="btn" onclick={() => void res.load()} disabled={res.loading}>Refresh tools</button>
  </div>
  <StateView {res} retry={() => res.load()}>
    {#snippet children(data)}
      <div class="flex flex-col gap-3 px-4 pb-4">
        <p class="small muted text-ui-sm">{wholeNumber(data.totalExecutions)} executions · {wholeNumber(data.recordedRuns)} recorded runs</p>
        {#if data.tools.length}
          <table class="w-full tool-usage">
            <caption class="sr-only">Most used tools by executions in the last {days} days</caption>
            <thead><tr><th scope="col" class="text-left">Tool</th><th scope="col" class="text-right">Executions</th></tr></thead>
            <tbody>
              {#each data.tools as tool (tool.name)}
                <tr>
                  <th scope="row" class="mono text-left font-normal break-all pr-4 py-2 w-1/3">{tool.name}</th>
                  <td class="py-2">
                    <div class="flex items-center gap-3">
                      <div class="flex-1 min-w-0" aria-hidden="true"><div class="tool-bar h-3 rounded-sm bg-[var(--chart-ink)]" style:width={`${100 * tool.executions / data.tools[0]!.executions}%`}></div></div>
                      <span class="tabular-nums text-right min-w-12">{wholeNumber(tool.executions)}</span>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}
          <p class="muted">No tool executions recorded in this period.</p>
        {/if}
      </div>
    {/snippet}
  </StateView>
</section>
