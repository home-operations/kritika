<script lang="ts">
  import type { ReviewDetail, RunnerRun } from '../../types';
  import { href } from '../../router.svelte';
  import { between, duration, tokens, usd, wholeNumber, bytes } from '../../format';
  import { clock } from '../../time.svelte';
  import Time from '../../components/Time.svelte';
  import ColumnChart from '../../components/ColumnChart.svelte';

  let { slug, d }: { slug: string; d: ReviewDetail } = $props();

  interface Segment {
    name: string;
    from: string;
    to: string | null;
    ms: number;
    cls: string;
  }

  function segments(r: RunnerRun, now: number): Segment[] {
    const out: Segment[] = [];
    const add = (name: string, cls: string, from: string | null, to: string | null) => {
      if (!from) return;
      out.push({ name, cls, from, to, ms: between(from, to ?? new Date(now).toISOString()) ?? 0 });
    };
    add('queued', 'seg-queued', r.createdAt, r.scheduledAt ?? r.startedAt ?? r.finishedAt);
    add('starting', 'seg-starting', r.scheduledAt, r.startedAt ?? r.finishedAt);
    add('running', 'seg-running', r.startedAt, r.finishedAt);
    return out;
  }

  const segs = $derived(d.runnerRun ? segments(d.runnerRun, clock.now) : []);
  const totalMs = $derived(segs.reduce((a, s) => a + s.ms, 0));
  const split = $derived((d.agentRun?.parts.length ?? 0) > 1);
  // A split review's steps are named by part and by step within the part.
  const steps = $derived.by(() => {
    const seen = new Map<number, number>();
    return (d.agentRun?.timeline ?? []).map((s) => {
      const n = seen.get(s.part) ?? 0;
      seen.set(s.part, n + 1);
      return {
        key: String(s.index),
        label: split ? `${s.part}.${n}` : String(s.index),
        values: [s.inputTokens + s.outputTokens],
        title: `${split ? `part ${s.part}, step ${n}` : `step ${s.index}`}: ${wholeNumber(s.inputTokens)} in, ${wholeNumber(s.outputTokens)} out, ${duration(s.durationMs)}, ${bytes(s.outputBytes)} tool output${s.tools.length ? `, ${s.tools.join(', ')}` : ''}`,
      };
    });
  });

  // dirs names the directories a part's files are in, the first three.
  function dirs(paths: string[]): string {
    const all = [...new Set(paths.map((p) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/') + 1) : './')))];
    return all.length > 3 ? `${all.slice(0, 3).join(', ')} and ${all.length - 3} more` : all.join(', ');
  }
</script>

{#if d.runnerRun}
  {@const r = d.runnerRun}
  <section class="panel" aria-labelledby="tl-runner">
    <header class="panel-head">
      <h2 id="tl-runner">Runner</h2>
      <span class="small muted">{r.phase}{totalMs ? ` · ${duration(totalMs)} total` : ''}</span>
    </header>
    {#if segs.length}
      <div class="phase-bar" role="img" aria-label={segs.map((s) => `${s.name} ${duration(s.ms)}`).join(', ')}>
        {#each segs as s (s.name)}
          <span class="phase-seg {s.cls}" style:flex-grow={Math.max(s.ms, 1)} title="{s.name}: {duration(s.ms)}">{s.name}</span>
        {/each}
      </div>
    {/if}
    <div class="table-wrap">
      <table class="data">
        <thead><tr><th scope="col">Phase</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Took</th></tr></thead>
        <tbody>
          {#each segs as s (s.name)}
            <tr><td>{s.name}</td><td><Time iso={s.from} full /></td><td><Time iso={s.to} full /></td><td>{s.to ? duration(s.ms) : `${duration(s.ms)} so far`}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <dl class="deflist">
      <dt>Job</dt><dd class="mono">{r.jobName || '—'}</dd>
      <dt>Pod</dt><dd class="mono">{r.podName || '—'}</dd>
      <dt>Node</dt><dd class="mono">{r.nodeName || '—'}</dd>
      <dt>Heartbeat</dt><dd><Time iso={r.heartbeatAt} /></dd>
      <dt>Exit code</dt><dd>{r.exitCode ?? '—'}</dd>
      <dt>Termination</dt><dd>{r.terminationReason || '—'}{r.deadlineExceeded ? ' (deadline exceeded)' : ''}</dd>
      {#if r.error}<dt>Error</dt><dd class="error-text">{r.error}</dd>{/if}
    </dl>
  </section>
{:else}
  <p class="state-msg">No runner was used for this review.</p>
{/if}

{#if d.agentRun}
  {@const a = d.agentRun}
  <section class="panel" aria-labelledby="tl-agent">
    <header class="panel-head">
      <h2 id="tl-agent">Agent steps</h2>
      <span class="small muted">{a.steps} steps{split ? ` in ${a.parts.length} parts` : ''} · {a.stopReason} · {usd(a.costUsd)} API spend</span>
    </header>
    {#if steps.length}
      <ColumnChart label="Tokens per agent step" series={[{ label: 'Tokens', color: 'var(--chart-ink)' }]} rows={steps} format={tokens} whole />
    {/if}
    {#if split}
      <div class="table-wrap">
        <table class="data" aria-label="Parts">
          <thead>
            <tr><th scope="col" class="num">Part</th><th scope="col">Files</th><th scope="col" class="num">Steps</th><th scope="col">Ended</th><th scope="col">Error</th></tr>
          </thead>
          <tbody>
            {#each a.parts as p, i (i)}
              {@const files = `${p.paths.length} ${p.paths.length === 1 ? 'file' : 'files'} in`}
              <tr>
                <td class="num">{i + 1}</td>
                <td class="name-fill" title="{files} {dirs(p.paths)}">{files} <span class="mono">{dirs(p.paths)}</span></td>
                <td class="num">{p.steps}</td>
                <td>{p.stop}</td>
                <td class="error-cell">{p.error}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    <dl class="deflist">
      <dt>Model</dt><dd class="mono">{a.model}</dd>
      {#if a.carriedReviewId}
        <dt>Conversation</dt>
        <dd>carried on from <a href={href({ name: 'review', slug, id: a.carriedReviewId, tab: 'conversation' })}>the review before</a></dd>
      {/if}
      <dt>Tokens</dt><dd>{tokens(a.usage.input)} in · {tokens(a.usage.cacheRead)} cache read · {tokens(a.usage.cacheWrite)} cache write · {tokens(a.usage.output)} out</dd>
      <dt>Tool calls</dt><dd class="mono">{Object.entries(a.toolCalls).map(([k, v]) => `${k}×${v}`).join(', ') || '—'}</dd>
      {#if a.skillsOffered.length}
        <dt>Skills</dt>
        <dd>
          {#each a.skillsOffered as s, i (s)}{i ? ', ' : ''}<span class="mono" class:muted={!a.skillsOpened.includes(s)}>{s}</span>{/each}
          <span class="small muted">({a.skillsOpened.length ? `read ${a.skillsOpened.join(', ')}` : 'none read'})</span>
        </dd>
      {/if}
      {#if a.commandsOffered.length}
        <dt>Commands</dt>
        <dd>
          {#each a.commandsOffered as c, i (c)}{i ? ', ' : ''}<span class="mono" class:muted={!a.commandsRun.includes(c)}>{c}</span>{/each}
          <span class="small muted">({a.commandsRun.length ? `ran ${a.commandsRun.join(', ')}` : 'none run'})</span>
        </dd>
      {/if}
      {#if a.sources.length}
        <dt>Sources</dt>
        <dd>
          <ul class="plain-list">
            {#each a.sources as s, i (i)}<li class="mono small">{s}</li>{/each}
          </ul>
        </dd>
      {/if}
      {#if a.error}<dt>Error</dt><dd class="error-text">{a.error}</dd>{/if}
    </dl>
  </section>
{/if}
