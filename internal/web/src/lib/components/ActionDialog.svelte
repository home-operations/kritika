<script lang="ts">
  // A state-changing action behind a confirm dialog: POST, announce the
  // result as a toast, then let the page refetch.
  import { ApiError, sendJSON } from '../api.svelte';
  import { describe } from '../manage';
  import { toast } from '../toast.svelte';
  import type { Accepted } from '../types';
  import Dialog from './Dialog.svelte';
  import { AlertDialog } from 'bits-ui';

  interface Props {
    open: boolean;
    label: string;
    title: string;
    body: string;
    path: string;
    danger?: boolean;
    // done names the result, for example "Re-run queued".
    done: string;
    // already, when set, is what to say when the server answers that the
    // same job is already queued, which is no failure.
    already?: string;
    ondone?: () => void;
  }
  let { open = $bindable(false), label, title, body, path, danger = false, done, already, ondone }: Props = $props();
  let busy = $state(false);

  async function run(): Promise<void> {
    busy = true;
    try {
      const res = await sendJSON<Accepted>('POST', path);
      toast(res?.jobId ? `${done}, job #${res.jobId}` : done);
    } catch (err) {
      if (already && err instanceof ApiError && err.code === 'already_queued') toast(already);
      else toast(`${label} failed: ${describe(err)}`, 'danger');
    } finally {
      busy = false;
      open = false;
      ondone?.();
    }
  }
</script>

<Dialog bind:open {title} alert={danger}>
  <p>{body}</p>
  {#snippet footer()}
    {#if danger}
      <AlertDialog.Cancel class="btn">Keep as is</AlertDialog.Cancel>
      <AlertDialog.Action class="btn btn-primary btn-danger" onclick={run} disabled={busy}>{busy ? 'Working…' : label}</AlertDialog.Action>
    {:else}
      <button class="btn" onclick={() => (open = false)}>Keep as is</button>
      <button class="btn btn-primary" onclick={run} disabled={busy}>{busy ? 'Working…' : label}</button>
    {/if}
  {/snippet}
</Dialog>
