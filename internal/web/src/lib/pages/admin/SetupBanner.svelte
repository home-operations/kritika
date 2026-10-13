<script lang="ts">
  // Tells an admin, on every page, while the instance cannot review, and
  // points at the Configuration page.
  import { getJSON } from '../../api.svelte';
  import { href } from '../../router.svelte';
  import { Resource } from '../../resource.svelte';
  import { notReviewing } from '../../setup';
  import type { SetupStatus } from '../../types';

  const status = new Resource(() => getJSON<SetupStatus>('/api/v1/admin/setup'));
  $effect(() => {
    void status.load();
  });
  const reason = $derived(status.data ? notReviewing(status.data) : '');
</script>

{#if reason}
  <div class="setup-banner flex items-center justify-between gap-3 mt-3 mx-4 mb-0 border border-solid border-line bg-surface rounded-md py-2 px-3 text-ui-md" role="note">
    <span>kritika cannot review yet: {reason}.</span>
    <a class="btn btn-small" href={href({ name: 'console' })}>See what is missing</a>
  </div>
{/if}
