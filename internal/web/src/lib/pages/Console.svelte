<script lang="ts">
  // The instance's administration, for admins: its accounts, or one of its
  // other pages.
  import PageHeader from '../components/PageHeader.svelte';
  import AuditTable from '../components/AuditTable.svelte';
  import type { ConsolePage } from '../routes';
  import AdminAccounts from './admin/AdminAccounts.svelte';
  import AdminSettings from './admin/AdminSettings.svelte';
  import AdminUsers from './admin/AdminUsers.svelte';
  import ConnectionsSection from './admin/ConnectionsSection.svelte';

  let { page }: { page?: ConsolePage } = $props();
  const TITLES: Record<ConsolePage | 'accounts', string> = { accounts: 'Accounts', connections: 'Connections', users: 'Users', settings: 'Settings', audit: 'Audit log' };
  const title = $derived(TITLES[page ?? 'accounts']);
</script>

<svelte:head><title>{title} · kritika</title></svelte:head>

<main id="main-content" class="page flex-auto min-w-0 min-h-0 overflow-auto relative">
  <div class="page-inner w-full my-0 mx-auto flex flex-col max-w-350 pt-6 px-7 pb-10 gap-5">
    <PageHeader {title} />
    {#if page === 'connections'}
      <ConnectionsSection />
    {:else if page === 'users'}
      <AdminUsers />
    {:else if page === 'settings'}
      <AdminSettings />
    {:else if page === 'audit'}
      <section class="panel" aria-labelledby="op-audit">
        <header class="panel-head"><h2 id="op-audit">Admin audit log</h2></header>
        <AuditTable path="/api/v1/admin/audit" showAccount />
      </section>
    {:else}
      <AdminAccounts />
    {/if}
  </div>
</main>
