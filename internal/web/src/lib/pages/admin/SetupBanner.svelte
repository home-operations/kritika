<script lang="ts">
  // Tells an admin, on every page, while the instance cannot review, and
  // points at the Configuration page.
  import { href } from '../../router.svelte';
  import { Resource } from '../../resource.svelte';
  import { notReviewing } from '../../setup';
  import type { SetupStatus } from '../../types';

  const status = Resource.json<SetupStatus>(() => '/api/v1/admin/setup');
  $effect(() => {
    void status.load();
  });
  const reason = $derived(status.data ? notReviewing(status.data) : '');
</script>

{#if reason}
  <div class="setup-banner flex items-center justify-between gap-3 mt-4 mx-7 mb-0 max-[800px]:mx-4 border border-solid border-line bg-surface rounded-md py-2.5 px-4 text-ui-md" role="note">
    <span>kritika cannot review yet: {reason}.</span>
    <a class="btn btn-small" href={href({ name: 'console' })}>See what is missing</a>
  </div>
{/if}
