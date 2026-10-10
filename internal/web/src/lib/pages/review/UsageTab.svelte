<script lang="ts">
  import type { ReviewDetail } from '../../types';
  import { callCost, tokens, usd, wholeNumber } from '../../format';
  import Time from '../../components/Time.svelte';
  import StatTile from '../../components/StatTile.svelte';

  let { d }: { d: ReviewDetail } = $props();
  const total = $derived(
    d.usage.reduce((a, u) => ({ in: a.in + u.inputTokens, out: a.out + u.outputTokens, cost: a.cost + u.costUsd }), { in: 0, out: 0, cost: 0 }),
  );
  const planUsage = $derived(d.usage.filter((u) => u.chatgptPlan));
  const planTotal = $derived(planUsage.reduce((a, u) => ({ in: a.in + u.inputTokens, out: a.out + u.outputTokens }), { in: 0, out: 0 }));
  const showRuns = $derived(d.usage.some((u) => u.runnerRunId));
  const planRuns = $derived.by(() => {
    const runs = new Map<string, { id: string; calls: number; input: number; output: number }>();
    for (const u of planUsage) {
      const id = u.runnerRunId ?? '';
      const run = runs.get(id) ?? { id, calls: 0, input: 0, output: 0 };
      run.calls++;
      run.input += u.inputTokens;
      run.output += u.outputTokens;
      runs.set(id, run);
    }
    return [...runs.values()];
  });
</script>

{#if d.usage.length === 0}
  <p class="state-msg">No usage recorded.</p>
{:else}
  {#if planUsage.length > 0}
    <section class="stats stats-3" aria-label="ChatGPT usage for this review">
      <StatTile label="ChatGPT calls" value={wholeNumber(planUsage.length)} sub="Across all runs of this review" />
      <StatTile label="ChatGPT input tokens" value={tokens(planTotal.in)} define={wholeNumber(planTotal.in)} sub="Includes cached tokens" />
      <StatTile label="ChatGPT output tokens" value={tokens(planTotal.out)} define={wholeNumber(planTotal.out)} sub="Included in plan" />
    </section>
    <section class="panel" aria-label="ChatGPT usage by run">
      <header class="panel-head"><h2>ChatGPT usage by run</h2></header>
      <div class="table-wrap">
        <table class="data">
          <thead><tr><th scope="col">Run</th><th scope="col" class="num">Calls</th><th scope="col" class="num">Input</th><th scope="col" class="num">Output</th><th scope="col" class="num">Total tokens</th></tr></thead>
          <tbody>
            {#each planRuns as run (run.id)}
              <tr>
                <td class="mono small" title={run.id || undefined}>{run.id ? `Run ${run.id.slice(0, 8)}` : 'Run not recorded'}</td>
                <td class="num">{wholeNumber(run.calls)}</td>
                <td class="num" title={wholeNumber(run.input)}>{tokens(run.input)}</td>
                <td class="num" title={wholeNumber(run.output)}>{tokens(run.output)}</td>
                <td class="num" title={wholeNumber(run.input + run.output)}>{tokens(run.input + run.output)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
  <div class="table-wrap table-card">
    <table class="data">
      <thead>
        <tr>
          <th scope="col">Role</th><th scope="col">Model</th><th scope="col">Upstream</th><th scope="col">Billing</th>{#if showRuns}<th scope="col">Run</th>{/if}
          <th scope="col" class="num">Input</th><th scope="col" class="num">Output</th><th scope="col" class="num">API spend</th><th scope="col">When</th>
        </tr>
      </thead>
      <tbody>
        {#each [...d.usage].reverse() as u, i (i)}
          <tr>
            <td>{u.role}</td>
            <td class="mono small">{u.model}</td>
            <td class="mono small">{u.upstream}</td>
            <td>{u.chatgptPlan ? 'ChatGPT plan' : 'API'}</td>
            {#if showRuns}<td class="mono small" title={u.runnerRunId}>{u.runnerRunId ? u.runnerRunId.slice(0, 8) : '—'}</td>{/if}
            <td class="num" title={wholeNumber(u.inputTokens)}>{tokens(u.inputTokens)}</td>
            <td class="num" title={wholeNumber(u.outputTokens)}>{tokens(u.outputTokens)}</td>
            <td class="num">{callCost(u.costUsd, u.chatgptPlan)}</td>
            <td><Time iso={u.createdAt} /></td>
          </tr>
        {/each}
      </tbody>
      <tfoot>
        <tr>
          <th scope="row" colspan={showRuns ? 5 : 4}>Total API spend</th>
          <td class="num" title={wholeNumber(total.in)}>{tokens(total.in)}</td>
          <td class="num" title={wholeNumber(total.out)}>{tokens(total.out)}</td>
          <td class="num">{usd(total.cost)}</td>
          <td></td>
        </tr>
      </tfoot>
    </table>
  </div>
  <p class="small muted">API spend excludes subscription fees.</p>
{/if}
