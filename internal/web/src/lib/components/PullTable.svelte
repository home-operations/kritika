<script lang="ts">
  // One row per pull request: its title over its repository, number and
  // author; its last review's findings and status.
  // `selected` marks the keyboard cursor; a click anywhere on a row that is
  // not a control opens its preview. With onpick, each row has a checkbox
  // for a bulk action, picked naming the ones checked by pullKey; a merged
  // or closed pull request takes no more reviews, so it has none.
  import { href, router } from '../router.svelte';
  import { rowToggles } from '../rowclick';
  import { pageIn } from '../sections';
  import { pullKey, pullRoute } from '../links';
  import type { Pull } from '../types';
  import { noMoreReviews, costTotal, wholeNumber } from '../format';
  import Icon from '../Icon.svelte';
  import { mdiLockOutline } from '../icons';
  import { lifecycle } from '../lifecycle';
  import ReviewStatusTile from './ReviewStatusTile.svelte';
  import SeverityCounts from './SeverityCounts.svelte';
  import RowToggle from './RowToggle.svelte';
  import PullPreview from './PullPreview.svelte';

  let {
    slug,
    items,
    selected = -1,
    picked = [],
    onpick,
    keyOf = pullKey,
  }: { slug?: string; items: (Pull & { account?: string })[]; keyOf?: (p: Pull & { account?: string }) => string; selected?: number; picked?: string[]; onpick?: (keys: string[], on: boolean) => void } = $props();

  const accountOf = (p: Pull & { account?: string }): string => slug ?? p.account!;
  const tableId = $props.id();
  let expanded = $state<Record<string, boolean>>({});
  const open = $derived(items.filter((p) => p.state === 'open'));


  function onRowClick(e: MouseEvent, key: string): void {
    if (rowToggles(e)) expanded[key] = !expanded[key];
  }
</script>

<div class="table-wrap table-card overflow-x-auto max-w-full bg-panel border border-solid border-line rounded-lg">
  <table class="data pull-table">
    <thead>
      <tr>
        {#if onpick}
          <th scope="col" class="pick relative">
            <label class="inline-flex items-center gap-2 font-normal">
              <input
                type="checkbox"
                aria-label="Select every pull request shown"
                checked={open.length > 0 && open.every((p) => picked.includes(keyOf(p)))}
                disabled={open.length === 0}
                onchange={(e) => onpick(open.map(keyOf), e.currentTarget.checked)}
              />
              <span class="pick-label sr-only">Select all</span>
            </label>
          </th>
        {/if}
        {#if !slug}<th scope="col">Account</th>{/if}
        <th scope="col">Pull request</th>
        <th scope="col" title="P0, P1 and P2, in that order">Findings</th>
        <th scope="col">Last review</th>
        <th scope="col" class="num" title="Reviews that completed; skipped ones are not counted">Reviews</th>
        <th scope="col" class="num" title="API spend across every review; subscription fees excluded">API spend</th>
        <th scope="col" class="relative"><span class="sr-only">Preview</span></th>
      </tr>
    </thead>
    <tbody class="pull-rows">
      {#each items as p, i (`${accountOf(p)}:${pullKey(p)}`)}
        {@const life = lifecycle(p)}
        {@const key = `${accountOf(p)}:${pullKey(p)}`}
        {@const previewId = `${tableId}-${i}`}
        <!-- The title is the row's link; the rest of the row toggles its preview. -->
        <tr class="pull-row row-toggles" class:selected={i === selected} class:row-expanded={expanded[key]} data-index={i} onclick={(e) => onRowClick(e, key)}>
          {#if onpick}
            <td class="pick">
              {#if p.state === 'open'}
                <input
                  type="checkbox"
                  aria-label="Select {p.repository}#{p.number}"
                  checked={picked.includes(keyOf(p))}
                  onchange={(e) => onpick([keyOf(p)], e.currentTarget.checked)}
                />
              {:else}
                <span class="muted" title={`${noMoreReviews(p.merged)}, from its page`}>
                  <Icon path={mdiLockOutline} size={13} label="Re-run from its page" />
                </span>
              {/if}
            </td>
          {/if}
          {#if !slug}<td data-label="Account" class="mono small text-ui-sm"><a href={href(pageIn(router.route, accountOf(p)))}>{accountOf(p)}</a></td>{/if}
          <td class="pull-main">
            <a class="pull-title inline-flex max-w-full items-center gap-1.5 min-w-0 align-top text-ink [font-weight:550] text-[length:15px] leading-[1.5]" href={href(pullRoute(accountOf(p), p))} aria-current={i === selected ? 'true' : undefined}>
              <!-- A row marks what is no longer simply open, and only a merge in colour: a list of red marks would shout. -->
              {#if life.state !== 'open'}<span class="lifecycle tone-{life.state === 'merged' ? life.tone : 'muted'}" title={life.label}><Icon path={life.icon} size={13} label={life.label} /></span>{/if}
              <span class="pull-text min-w-0 overflow-hidden text-ellipsis">{p.title}</span>
            </a>
            <span class="pull-sub block mt-0.5 overflow-hidden text-ellipsis text-ui-xs text-muted"><span class="mono">{p.repository}</span> · #{p.number} · {p.author}</span>
          </td>
          <td>
            {#if p.lastReview}
              <SeverityCounts counts={p.lastReview.findings} />
            {/if}
          </td>
          <td>
            {#if p.lastReview}
              <ReviewStatusTile status={p.lastReview.status} title="Last review" />
            {:else}
              <span class="small muted text-ui-sm">not reviewed</span>
            {/if}
            {#if p.paused && p.state === 'open'}
              <span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Automatic reviews are paused: a push is not reviewed until someone asks for a review or resumes them">paused</span>
            {/if}
          </td>
          <td class="num" data-label="Reviews">{wholeNumber(p.reviewCount)}</td>
          <td class="num" data-label="API spend">{costTotal(p.costUsd, p.unpricedCalls)}</td>
          <td class="row-toggle-cell"><RowToggle bind:open={expanded[key]} label={`Preview ${p.repository}#${p.number}${slug ? '' : ` in ${accountOf(p)}`}`} controls={previewId} /></td>
        </tr>
        {#if expanded[key]}
          <tr class="row-preview"><td colspan={6 + Number(!!onpick) + Number(!slug)}>
            <div class="row-preview-body" id={previewId} role="region" aria-label={`Preview of ${p.repository}#${p.number}`}>
              {#if p.lastReview}<PullPreview slug={accountOf(p)} pull={p} />
              {:else}<p class="muted">Not reviewed yet.</p><a class="btn btn-small mt-3" href={href(pullRoute(accountOf(p), p))}>Open pull request →</a>{/if}
            </div>
          </td></tr>
        {/if}
      {/each}
    </tbody>
  </table>
</div>
