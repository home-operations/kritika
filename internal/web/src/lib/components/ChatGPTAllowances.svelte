<script lang="ts">
  import { getJSON } from '../api.svelte';
  import { accountApi } from '../links';
  import { Resource, live } from '../resource.svelte';
  import { clock, ago } from '../time.svelte';
  import type { ChatGPTAllowanceWindow, ChatGPTProviderUsage } from '../types';
  import Meter from './Meter.svelte';
  import StateView from './StateView.svelte';
  import Time from './Time.svelte';

  let { slug }: { slug: string } = $props();
  const res = new Resource(() => getJSON<ChatGPTProviderUsage[]>(`${accountApi(slug)}/chatgpt/allowances`));
  $effect(() => { void res.load(); });
  $effect(() => live((e) => e.account === slug && e.kind === 'model_call', () => void res.load(), 2000));

  function windowName(w: ChatGPTAllowanceWindow): string {
    const mins = w.windowDurationMins;
    if (mins === null) return 'Allowance';
    if (mins === 10080) return 'Weekly allowance';
    if (mins % 1440 === 0) return `${mins / 1440}-day allowance`;
    if (mins % 60 === 0) return `${mins / 60}-hour allowance`;
    return `${mins}-minute allowance`;
  }

  function expired(w: ChatGPTAllowanceWindow): boolean {
    return w.resetsAt !== null && w.resetsAt * 1000 <= clock.now;
  }

  function resetTime(w: ChatGPTAllowanceWindow): string | null {
    if (w.resetsAt === null) return null;
    const date = new Date(w.resetsAt * 1000);
    return Number.isFinite(date.getTime()) ? date.toISOString() : null;
  }

  const remaining = (w: ChatGPTAllowanceWindow) => Math.max(0, 100 - w.usedPercent).toLocaleString('en', { maximumFractionDigits: 1 });
</script>

<section class="panel" aria-label="ChatGPT allowances">
  <header class="panel-head">
    <h2>ChatGPT allowances</h2>
    <a class="small" href="https://chatgpt.com/settings/usage" target="_blank" rel="noopener noreferrer">Manage ChatGPT usage</a>
  </header>
  <div class="panel-body">
    <StateView {res} retry={() => res.load()}>
      {#snippet children(providers)}
        {#each providers as p (p.provider)}
          <div class="allowance-provider">
            <h3 class="mono small">{p.provider}</h3>
            {#if !p.connected}
              <p class="small muted">ChatGPT is not connected.</p>
            {:else if p.allowances.length === 0}
              <p class="small muted">Allowance data unavailable. OpenAI has not supplied quota data for this connection.</p>
            {:else}
              {#each p.allowances as a (a.limitId)}
                <div class="stats stats-3" aria-label={a.limitId === 'codex' ? `${p.provider} allowances` : `${p.provider} ${a.limitId} allowances`}>
                  {#each [a.primary, a.secondary].filter((w): w is ChatGPTAllowanceWindow => w !== null) as w, i (i)}
                    {@const reset = resetTime(w)}
                    <div class="stat">
                      <span class="stat-label">{windowName(w)}</span>
                      <span class="stat-value">{remaining(w)}% remaining</span>
                      <Meter value={Math.min(100, w.usedPercent)} max={100} label={`${windowName(w)} used`} />
                      <span class="stat-sub">
                        {#if expired(w)}Reset time passed; awaiting updated usage
                        {:else if reset}Resets {ago(reset)}
                        {:else}Reset time unavailable{/if}
                      </span>
                      {#if reset}<span class="small muted"><Time iso={reset} full /></span>{/if}
                    </div>
                  {/each}
                </div>
                <p class="small muted">{a.limitId !== 'codex' ? `${a.limitId} · ` : ''}Last reported <Time iso={a.observedAt} />. Updates when OpenAI returns quota data.</p>
              {/each}
            {/if}
          </div>
        {/each}
      {/snippet}
    </StateView>
  </div>
</section>

<style>
  .allowance-provider + .allowance-provider { margin-top: 1.5rem; }
  h3 { margin: 0 0 0.75rem; }
</style>
