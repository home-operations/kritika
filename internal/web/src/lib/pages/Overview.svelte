<script lang="ts">
  // The instance's landing page: totals across every account the viewer can
  // see, then one row per account with its numbers and its health, each
  // cell that names something wrong leading to where the account shows it.
  import { getJSON } from '../api.svelte';
  import { href } from '../router.svelte';
  import { Resource, live } from '../resource.svelte';
  import { tokens, usd, wholeNumber } from '../format';
  import type { AccountSummary } from '../types';
  import { WANTS, nearCaps } from '../attention';
  import StateView from '../components/StateView.svelte';
  import Meter from '../components/Meter.svelte';
  import Pill from '../components/Pill.svelte';
  import WebhookState from '../components/WebhookState.svelte';

  const res = new Resource(() => getJSON<AccountSummary[]>('/api/v1/accounts'));

  $effect(() => {
    void res.load();
  });
  $effect(() => live((e) => e.kind !== 'model_call', () => void res.load()));

  function totals(list: AccountSummary[]) {
    const sum = (f: (t: AccountSummary) => number) => list.reduce((n, t) => n + f(t), 0);
    return {
      repositories: sum((t) => t.repositories),
      reviews7d: sum((t) => t.reviews7d),
      reviewsToday: sum((t) => t.usage.reviewsToday),
      tokens: sum((t) => t.usage.tokens),
      costUsd: sum((t) => t.usage.costUsd),
    };
  }
</script>

<svelte:head><title>All accounts · kritika</title></svelte:head>

<main class="page">
  <div class="page-inner">
    <header class="page-head"><h1>All accounts</h1></header>
    <StateView {res} retry={() => res.load()} isEmpty={(d) => d.length === 0} empty="You are not a member of any account yet.">
      {#snippet children(list)}
        {@const all = totals(list)}
        <section class="tiles" aria-label="Across all accounts">
          <div class="tile">
            <span class="tile-label">Accounts</span>
            <span class="tile-value">{wholeNumber(list.length)}</span>
          </div>
          <div class="tile">
            <span class="tile-label">Repositories</span>
            <span class="tile-value">{wholeNumber(all.repositories)}</span>
          </div>
          <div class="tile">
            <span class="tile-label">Reviews, last 7 days</span>
            <span class="tile-value">{wholeNumber(all.reviews7d)}</span>
            <span class="small muted">{wholeNumber(all.reviewsToday)} today</span>
          </div>
          <div class="tile">
            <span class="tile-label">Spend this month</span>
            <span class="tile-value">{usd(all.costUsd)}</span>
          </div>
          <div class="tile">
            <span class="tile-label">Tokens this month</span>
            <span class="tile-value" title={wholeNumber(all.tokens)}>{tokens(all.tokens)}</span>
          </div>
        </section>

        <section class="panel" aria-labelledby="account-breakdown">
          <header class="panel-head"><h2 id="account-breakdown">By account</h2></header>
          <div class="table-wrap">
            <table class="data account-breakdown">
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Connection</th>
                  <th scope="col" class="num">Repositories</th>
                  <th scope="col" class="num">Reviews 7d</th>
                  <th scope="col" title="Open pull requests whose last review failed, hit a limit or found something blocking, or whose automatic reviews are paused; and a cap that is close">Needs attention</th>
                  <th scope="col" title="Whether GitHub's webhooks reach kritika, and when it last polled instead">Webhooks</th>
                  <th scope="col" class="num">Spend</th>
                  <th scope="col">Tokens this month</th>
                </tr>
              </thead>
              <tbody>
                {#each list as t (t.slug)}
                  <tr>
                    <td class="mono"><a href={href({ name: 'account', slug: t.slug })}>{t.slug}</a></td>
                    <td class="mono small">{t.connection}</td>
                    <td class="num">{wholeNumber(t.repositories)}</td>
                    <td class="num">{wholeNumber(t.reviews7d)}</td>
                    <td>
                      <span class="account-wants">
                        {#each WANTS.filter((w) => t.attention[w.key] > 0) as w (w.key)}
                          <a href={href({ name: 'pulls', slug: t.slug, filter: w.filter })} title="{t.attention[w.key]} open pull requests {w.why}">
                            <Pill tone={w.tone} label={`${t.attention[w.key]} ${w.label}`} />
                          </a>
                        {/each}
                        {#each nearCaps(t.usage) as text (text)}
                          <a href={href({ name: 'usage', slug: t.slug })} title={text}><Pill tone="warn" label="cap" /></a>
                        {/each}
                        {#if !WANTS.some((w) => t.attention[w.key] > 0) && !nearCaps(t.usage).length}<span class="muted">—</span>{/if}
                      </span>
                    </td>
                    <td>
                      <WebhookState of={t} polled={{ at: t.lastPolledAt }} />
                    </td>
                    <td class="num">{usd(t.usage.costUsd)}</td>
                    <td>
                      <span class="small">
                        {tokens(t.usage.tokens)}{t.usage.tokensPerMonth ? ` of ${tokens(t.usage.tokensPerMonth)}` : ''}
                      </span>
                      {#if t.usage.tokensPerMonth}
                        <Meter value={t.usage.tokens} max={t.usage.tokensPerMonth} label={`Monthly tokens used by ${t.slug}`} />
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/snippet}
    </StateView>
  </div>
</main>
