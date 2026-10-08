<script lang="ts">
  // Why a merged or closed pull request takes no automatic reviews, beside
  // the action that asks for one.
  import Icon from '../Icon.svelte';
  import { mdiLockOutline } from '../icons';
  import { noMoreReviews } from '../format';
  let { merged }: { merged: boolean } = $props();
</script>

<p class="small muted no-more-reviews">
  <Icon path={mdiLockOutline} size={13} />
  {noMoreReviews(merged)}.
</p>
