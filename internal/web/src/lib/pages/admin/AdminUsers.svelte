<script lang="ts">
  // Everyone who has signed in, with how they sign in and what their
  // session reads; an admin can sign one out everywhere.
  import { AlertDialog } from 'bits-ui';
  import { sendJSON } from '../../api.svelte';
  import { Resource } from '../../resource.svelte';
  import { describe } from '../../manage';
  import { toast } from '../../toast.svelte';
  import { session } from '../../session.svelte';
  import { wholeNumber } from '../../format';
  import type { AdminUser } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import HelpPopover from '../../components/HelpPopover.svelte';
  import Pill from '../../components/Pill.svelte';
  import Time from '../../components/Time.svelte';
  import Dialog from '../../components/Dialog.svelte';

  const users = Resource.json<AdminUser[]>(() => '/api/v1/admin/users');
  $effect(() => {
    void users.load();
  });

  const name = (u: AdminUser) => u.displayName || u.email || u.signIns[0]?.login || 'this user';

  let leaving = $state<AdminUser | undefined>(undefined);
  let confirmOpen = $state(false);
  let busy = $state(false);
  const self = $derived(leaving !== undefined && leaving.id === session.me?.user.id);

  function ask(u: AdminUser): void {
    leaving = u;
    confirmOpen = true;
  }

  // An admin who signs themselves out finds the list's reload refused,
  // which takes them to the sign-in page.
  async function signOut(): Promise<void> {
    const u = leaving;
    if (!u) return;
    busy = true;
    try {
      await sendJSON('DELETE', `/api/v1/admin/users/${encodeURIComponent(u.id)}/sessions`);
      toast(`Signed ${name(u)} out everywhere`);
    } catch (err) {
      toast(`Sign out failed: ${describe(err)}`, 'danger');
    } finally {
      busy = false;
      confirmOpen = false;
      void users.load();
    }
  }
</script>

<section class="panel" aria-labelledby="op-users">
  <header class="panel-head">
    <h2 id="op-users">Users</h2>
    <HelpPopover label="About users">
      <p>Everyone who has signed in. Access is what their newest session reads, as their sign-in decided it; it changes when they sign in again.</p>
    </HelpPopover>
  </header>
  <StateView res={users} retry={() => users.load()} isEmpty={(d) => d.length === 0} empty="No one has signed in yet.">
    {#snippet children(list)}
      <div class="table-wrap overflow-x-auto max-w-full">
        <table class="data user-table">
          <thead>
            <tr>
              <th scope="col">User</th>
              <th scope="col">Signs in with</th>
              <th scope="col">Access</th>
              <th scope="col" class="num" title="Sessions still live">Sessions</th>
              <th scope="col">Last sign-in</th>
              <th scope="col">First seen</th>
              <th scope="col" class="relative"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {#each list as u (u.id)}
              <tr>
                <td class="name-fill" title={u.email || undefined}>
                  <span class="block">{u.displayName || u.email || '—'}</span>
                  {#if u.displayName && u.email}<span class="block small muted text-ui-sm">{u.email}</span>{/if}
                </td>
                <td>
                  <span class="inline-flex flex-wrap gap-1">
                    {#each u.signIns as s (`${s.provider}\u0000${s.login}`)}
                      <span class="badge mono inline-flex items-center text-ui-xs border border-solid border-line py-0 px-[7px] text-muted rounded-[3px]">{s.login ? `${s.provider}: ${s.login}` : s.provider}</span>
                    {/each}
                  </span>
                </td>
                <td class="wrap">
                  {#if !u.access}<span class="muted small text-ui-sm">no active session</span>
                  {:else if u.access.admin}<Pill tone="accent" label="admin" />
                  {:else if u.access.allAccounts}<span>every account</span>
                  {:else if u.access.accounts.length}<span class="mono small text-ui-sm">{u.access.accounts.join(', ')}</span>
                  {:else}<span class="muted small text-ui-sm">no account</span>{/if}
                </td>
                <td class="num">{wholeNumber(u.sessions)}</td>
                <td><Time iso={u.lastSignInAt} /></td>
                <td><Time iso={u.firstSeenAt} /></td>
                <td>
                  <button class="btn btn-small" disabled={u.sessions === 0} aria-label="Sign {name(u)} out everywhere" onclick={() => ask(u)}>Sign out everywhere</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/snippet}
  </StateView>
</section>

<Dialog alert bind:open={confirmOpen} title="Sign out everywhere?" fallback="#op-users">
  <p>
    This ends every session of {leaving ? name(leaving) : 'this user'}.
    {#if self}That includes yours, so you go back to the sign-in page.{:else}They can sign in again while their sign-in allows it.{/if}
  </p>
  {#snippet footer()}
    <AlertDialog.Cancel class="btn">Keep them</AlertDialog.Cancel>
    <AlertDialog.Action class="btn btn-primary btn-danger" onclick={signOut} disabled={busy}>{busy ? 'Working…' : 'Sign out'}</AlertDialog.Action>
  {/snippet}
</Dialog>
