<script lang="ts">
  // Personal access tokens, with their owners in an admin's list, each
  // revocable once confirmed.
  import { AlertDialog } from 'bits-ui';
  import { sendJSON } from '../api.svelte';
  import { describe } from '../manage';
  import { toast } from '../toast.svelte';
  import type { APIToken, TokenStatus } from '../types';
  import Dialog from './Dialog.svelte';
  import Pill from './Pill.svelte';
  import Time from './Time.svelte';

  // revokePath is where a token's revocation is sent; onrevoked reloads the
  // list after one; fallback is where focus goes once the dialog closes,
  // when the token's row has gone with its Revoke button.
  let { tokens, owners = false, revokePath, onrevoked, fallback }: {
    tokens: APIToken[];
    owners?: boolean;
    revokePath: (id: string) => string;
    onrevoked: (id: string) => Promise<void>;
    fallback: string;
  } = $props();

  const STATUS: Record<TokenStatus, { tone: 'ok' | 'muted' | 'warn'; title: string }> = {
    active: { tone: 'ok', title: 'It reads until it expires or is revoked' },
    expired: { tone: 'muted', title: 'It passed its expiry and reads nothing' },
    stale: { tone: 'warn', title: 'The sign-in it came from has changed, so it reads nothing: create another' },
  };

  let revoking = $state<APIToken | undefined>(undefined);
  let confirmOpen = $state(false);
  let busy = $state(false);

  function ask(t: APIToken): void {
    revoking = t;
    confirmOpen = true;
  }

  // After a revocation the list reloads before the dialog closes, so that
  // focus goes to the fallback, not to a Revoke button about to leave with
  // its row. One that fails leaves the list, and a token's reveal, as they
  // were.
  async function revoke(): Promise<void> {
    const t = revoking;
    if (!t) return;
    busy = true;
    try {
      await sendJSON('DELETE', revokePath(t.id));
      toast(`Revoked ${t.name}`);
      await onrevoked(t.id);
    } catch (err) {
      toast(`Revoke failed: ${describe(err)}`, 'danger');
    } finally {
      busy = false;
      confirmOpen = false;
    }
  }
</script>

<div class="table-wrap overflow-x-auto max-w-full">
  <table class="data token-table">
    <thead>
      <tr>
        <th scope="col">Token</th>
        {#if owners}<th scope="col">Owner</th>{/if}
        <th scope="col">Reads</th>
        <th scope="col">Status</th>
        <th scope="col">Created</th>
        <th scope="col">Last used</th>
        <th scope="col">Expires</th>
        <th scope="col" class="relative"><span class="sr-only">Actions</span></th>
      </tr>
    </thead>
    <tbody>
      {#each tokens as t (t.id)}
        <tr>
          <td class="name-fill" title={t.name}>{t.name}</td>
          {#if owners}<td class="name-clip" title={t.owner?.email || undefined}>{t.owner?.displayName || t.owner?.email || '—'}</td>{/if}
          <td class="wrap">
            {#if t.allAccounts}every account{:else if t.accounts.length}<span class="mono small text-ui-sm">{t.accounts.join(', ')}</span>{:else}<span class="muted small text-ui-sm">no account</span>{/if}
          </td>
          <td><Pill tone={STATUS[t.status].tone} label={t.status} title={STATUS[t.status].title} /></td>
          <td><Time iso={t.createdAt} /></td>
          <td>{#if t.lastUsedAt}<Time iso={t.lastUsedAt} />{:else}<span class="muted small text-ui-sm">never</span>{/if}</td>
          <td>{#if t.expiresAt}<Time iso={t.expiresAt} />{:else}<span class="small text-ui-sm">never</span>{/if}</td>
          <td><button class="btn btn-small" aria-label="Revoke {t.name}" onclick={() => ask(t)}>Revoke</button></td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<Dialog alert bind:open={confirmOpen} title="Revoke the token?" {fallback}>
  <p>An agent using {revoking?.name ?? 'this token'} can no longer read with it. This cannot be undone.</p>
  {#snippet footer()}
    <AlertDialog.Cancel class="btn">Keep it</AlertDialog.Cancel>
    <AlertDialog.Action class="btn btn-primary btn-danger" onclick={revoke} disabled={busy}>{busy ? 'Working…' : 'Revoke'}</AlertDialog.Action>
  {/snippet}
</Dialog>
