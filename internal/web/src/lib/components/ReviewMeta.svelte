<script lang="ts">
  // The facts about a review run, each with its name: trigger, scope, model,
  // confidence, risk, head, cost, tokens, duration. Shared by the pull request page and
  // the review page.
  import type { Review } from '../types';
  import { callCost, duration, tokens, wholeNumber, shortSha } from '../format';
  let { r, scopeReason = '' }: { r: Review; scopeReason?: string } = $props();
</script>

<dl class="facts">
  <div><dt>Trigger</dt><dd>{r.trigger}</dd></div>
  <div><dt>Scope</dt><dd>{r.scope}{#if scopeReason}&nbsp;<span class="muted">({scopeReason})</span>{/if}</dd></div>
  {#if r.model}<div><dt>Model</dt><dd class="mono">{r.model}</dd></div>{/if}
  {#if r.confidence}
    <div>
      <dt>Confidence</dt>
      <dd title="{r.confidence.reason} ({r.confidence.model})">
        {r.confidence.score}/5{#if !r.confidence.passed}&nbsp;<span class="muted">(below {r.confidence.threshold})</span>{/if}
      </dd>
    </div>
    {#if r.confidence.risk}<div><dt>Risk</dt><dd>{r.confidence.risk}</dd></div>{/if}
  {/if}
  <div><dt>Head</dt><dd class="mono" title={r.headSha}>{shortSha(r.headSha)}</dd></div>
  <div>
    <dt>API spend</dt>
    <dd>
      {callCost(r.costUsd, !!r.planCalls && r.planCalls === r.calls)}
      {#if r.planCalls}<span class="badge">ChatGPT plan</span>{/if}
    </dd>
  </div>
  <div>
    <dt>Tokens</dt>
    <dd title="{wholeNumber(r.tokens.input)} in / {wholeNumber(r.tokens.output)} out">{tokens(r.tokens.input)} in · {tokens(r.tokens.output)} out</dd>
  </div>
  {#if r.durationMs !== null}<div><dt>Took</dt><dd>{duration(r.durationMs)}</dd></div>{/if}
</dl>
