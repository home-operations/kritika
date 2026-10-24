<script lang="ts">
  // The viewer's personal access tokens, for agents that read through the
  // MCP endpoint: each reads what their sign-in lets them read, or chosen
  // accounts among those, until it expires or is revoked.
  import { sendJSON } from '../../api.svelte';
  import { Resource } from '../../resource.svelte';
  import { describe } from '../../manage';
  import { session } from '../../session.svelte';
  import type { APIToken, CreatedToken, CreateTokenRequest } from '../../types';
  import StateView from '../../components/StateView.svelte';
  import TokenTable from '../../components/TokenTable.svelte';
  import Copy from '../../components/Copy.svelte';

  const tokens = Resource.json<APIToken[]>(() => '/api/v1/me/tokens');
  $effect(() => {
    void tokens.load();
  });

  const EXPIRIES = [
    { value: 7, label: '7 days' },
    { value: 30, label: '30 days' },
    { value: 90, label: '90 days' },
    { value: 365, label: '1 year' },
    { value: 0, label: 'Never' },
  ] as const;

  let name = $state('');
  let some = $state(false);
  let chosen = $state<string[]>([]);
  let expiresInDays = $state(30);
  let creating = $state(false);
  let error = $state('');
  let created = $state<CreatedToken | undefined>(undefined);
  const accounts = $derived(session.me?.accounts ?? []);
  // Characters as the API counts them: code points, once trimmed.
  const tooLong = $derived([...name.trim()].length > 100);

  // A revoked token's value is no use: its reveal goes with it.
  function revoked(id: string): Promise<void> {
    if (created?.id === id) created = undefined;
    return tokens.load();
  }

  async function create(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    error = '';
    creating = true;
    try {
      const body: CreateTokenRequest = { name: name.trim(), accounts: some ? chosen : [], expiresInDays };
      created = await sendJSON<CreatedToken>('POST', '/api/v1/me/tokens', body);
      name = '';
      some = false;
      chosen = [];
      expiresInDays = 30;
      void tokens.load();
    } catch (err) {
      error = describe(err);
    } finally {
      creating = false;
    }
  }
</script>

<section class="panel" aria-labelledby="pref-tokens">
  <header class="panel-head"><h2 id="pref-tokens">API tokens</h2></header>
  <p class="small muted text-ui-sm my-3 mx-4">A token lets an agent read what you can, or only the accounts you choose, through kritika's MCP endpoint. It cannot change anything.</p>
  {#if created}
    <div class="token-created flex flex-col gap-2 my-3 mx-4" role="status">
      <p class="m-0 text-ui-sm"><strong>{created.name}</strong> is ready. Copy it now: it is not shown again.</p>
      <div class="flex items-center gap-2 min-w-0">
        <code class="mono text-ui-sm wrap-anywhere">{created.token}</code>
        <Copy text={created.token} label="Copy the token" />
      </div>
      <div><button class="btn btn-small" onclick={() => (created = undefined)}>Done</button></div>
    </div>
  {/if}
  <form class="token-form flex flex-col gap-3 my-3 mx-4 text-ui-sm" aria-label="Create a token" onsubmit={create}>
    <label class="flex flex-col gap-1">Name
      <input type="text" required placeholder="laptop agent" aria-invalid={tooLong} bind:value={name} />
    </label>
    {#if tooLong}<p class="error-text text-danger text-ui-sm m-0">A name is at most 100 characters.</p>{/if}
    <fieldset class="flex flex-col gap-1 border-0 p-0 m-0">
      <legend class="mb-1">Reads</legend>
      <label class="flex items-center gap-2"><input type="radio" name="token-reads" checked={!some} onchange={() => (some = false)} /> Everything you can read</label>
      <label class="flex items-center gap-2"><input type="radio" name="token-reads" checked={some} onchange={() => (some = true)} /> Only these accounts</label>
      {#if some}
        <div class="flex flex-col gap-1 pl-6">
          {#each accounts as slug (slug)}
            <label class="flex items-center gap-2 mono"><input type="checkbox" value={slug} bind:group={chosen} /> {slug}</label>
          {/each}
        </div>
      {/if}
    </fieldset>
    <label class="flex flex-col gap-1">Expires
      <select class="self-start" bind:value={expiresInDays}>
        {#each EXPIRIES as x (x.value)}<option value={x.value}>{x.label}</option>{/each}
      </select>
    </label>
    {#if error}<p class="error-text text-danger text-ui-sm m-0" role="alert">{error}</p>{/if}
    <div>
      <button class="btn btn-primary" type="submit" disabled={creating || !name.trim() || tooLong || (some && chosen.length === 0)}>{creating ? 'Creating…' : 'Create token'}</button>
    </div>
  </form>
  <StateView res={tokens} retry={() => tokens.load()} isEmpty={(d) => d.length === 0} empty="You have no tokens.">
    {#snippet children(list)}
      <TokenTable tokens={list} revokePath={(id) => `/api/v1/me/tokens/${encodeURIComponent(id)}`} onrevoked={revoked} fallback="#pref-tokens" />
    {/snippet}
  </StateView>
</section>
