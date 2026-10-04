<script lang="ts">
  // Why a pull request takes no more reviews, where the action to ask for
  // one would be: a merged one never does, a closed one not while it is.
  import Icon from '../Icon.svelte';
  import { mdiLockOutline } from '../icons';
  let { merged }: { merged: boolean } = $props();
</script>

<p class="small muted no-more-reviews">
  <Icon path={mdiLockOutline} size={13} />
  {merged ? 'Merged: it is not reviewed again.' : 'Closed: it is not reviewed while it is.'}
</p>
