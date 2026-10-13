<script lang="ts">
  import PageHeader from '../../components/PageHeader.svelte';
  import { accountApi } from '../../links';
  import { isAdmin, session } from '../../session.svelte';
  import AuditTable from '../../components/AuditTable.svelte';
  import Spinner from '../../Spinner.svelte';

  let { slug, section }: { slug: string; section?: string } = $props();

  const SECTIONS: Record<string, string> = { audit: 'Audit log' };
  const current = $derived(section ?? 'audit');
  const title = $derived(Object.hasOwn(SECTIONS, current) ? SECTIONS[current] : 'Settings');
</script>

<svelte:head><title>{title} · {slug} · kritika</title></svelte:head>

<main class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader {title} {slug} />
    {#if !session.me}
      <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted" aria-live="polite"><Spinner size={16} /> Loading…</p>
    {:else if !isAdmin()}
      <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted" role="alert">Only an admin can see this page.</p>
    {:else if !Object.hasOwn(SECTIONS, current)}
      <p class="state-msg flex items-center gap-2 m-0 p-3 text-muted">No such section.</p>
    {:else}
      <section class="panel bg-panel border border-solid border-line rounded-lg min-w-0">
        <AuditTable path={`${accountApi(slug)}/audit`} />
      </section>
    {/if}
  </div>
</main>
