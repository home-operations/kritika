<script lang="ts">
  import { Resource } from '../resource.svelte';
  import { accountApi, repoPath, repoRoute } from '../links';
  import { href } from '../router.svelte';
  import { shortSha, wholeNumber } from '../format';
  import type { RepoDetail, Repository } from '../types';
  import StateView from './StateView.svelte';
  import Time from './Time.svelte';

  let { slug, repo }: { slug: string; repo: Repository } = $props();
  const res = Resource.live<RepoDetail>(() => `${accountApi(slug)}/repos/${repoPath(repo.fullName)}`);
  $effect(() => { void repo.enabled; void res.load(); });
</script>

<StateView {res} layout="inline" retry={() => res.load()}>
  {#snippet children(d)}
    {@const settings = d.repoConfig?.settings ?? d.settings}
    {@const run = d.indexRuns[0]}
    <div class="row-preview-grid">
      <div class="min-w-0">
        <h2 class="preview-heading">Review settings</h2>
        <dl class="preview-facts">
          <dt>Reviews</dt><dd>{d.enabled && (d.repoConfig?.settings.enabled ?? true) ? 'Enabled' : 'Disabled'}</dd>
          <dt>Default branch</dt><dd class="mono">{d.defaultBranch}</dd>
          <dt>Review model</dt><dd class="mono">{settings.models.review || 'Not configured'}</dd>
        </dl>
        {#if d.repoConfig?.ignored}<p class="text-danger wrap-anywhere mt-1">Repository configuration ignored: {d.repoConfig.ignored}</p>{/if}
      </div>
      <div class="min-w-0">
        <h2 class="preview-heading">Code index</h2>
        <dl class="preview-facts">
          <dt>Indexed commit</dt><dd class="mono" title={d.index.activeCommit ?? undefined}>{d.index.activeCommit ? shortSha(d.index.activeCommit) : 'Not indexed'}</dd>
          {#if run}
            <dt>Latest run</dt><dd>{run.status} · {run.mode}</dd>
            <dt>Started</dt><dd><Time iso={run.createdAt} /></dd>
            <dt>Chunks</dt><dd>{wholeNumber(run.chunkCount)}</dd>
          {/if}
        </dl>
        {#if run?.error}<p class="text-danger line-clamp-2 wrap-anywhere mt-1">{run.error}</p>{/if}
      </div>
    </div>
    <div class="preview-actions"><a class="btn btn-small" href={href(repoRoute(slug, repo.fullName))}>Open repository →</a></div>
  {/snippet}
</StateView>
