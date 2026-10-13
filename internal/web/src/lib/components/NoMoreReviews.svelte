<script lang="ts">
  // Why a merged or closed pull request takes no automatic reviews, beside
  // the action that asks for one.
  import Icon from '../Icon.svelte';
  import { mdiLockOutline } from '../icons';
  import { noMoreReviews } from '../format';
  let { merged }: { merged: boolean } = $props();
</script>

<p class="small muted no-more-reviews flex items-center gap-1.5 m-0 text-ui-sm">
  <Icon path={mdiLockOutline} size={13} />
  {noMoreReviews(merged)}.
</p>
