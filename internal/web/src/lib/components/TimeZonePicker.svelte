<script lang="ts">
  import { Combobox } from 'bits-ui';

  let { value, zones, onchange }: { value: string; zones: string[]; onchange: (value: string) => void } = $props();
  let open = $state(false);
  let search = $state('');
  const options = $derived([{ value: 'auto', label: 'Auto' }, ...zones.map((zone) => ({ value: zone, label: zone }))]);
  const matches = $derived(options.filter((option) => option.label.replaceAll('_', ' ').toLowerCase().includes(search.replaceAll('_', ' ').toLowerCase())));
</script>

<Combobox.Root
  type="single"
  value={value || 'auto'}
  inputValue={open ? search : value || 'Auto'}
  bind:open
  onOpenChange={(now) => { if (!now) search = ''; }}
  onValueChange={(zone) => { if (zone) onchange(zone === 'auto' ? '' : zone); }}
  allowDeselect={false}
>
  <div class="search-combo zone-picker relative w-[min(100%,_320px)]">
    <Combobox.Input
      id="pref-zone"
      autocomplete="off"
      placeholder="Search time zones"
      onclick={() => (open = true)}
      aria-controls={open ? 'pref-zone-options' : undefined}
      onfocus={() => (open = true)}
      oninput={(e) => { open = true; search = e.currentTarget.value; }}
    />
    <Combobox.ContentStatic id="pref-zone-options" class="suggest zone-options absolute top-[calc(100%_+_4px)] left-0 right-0 z-[var(--z-popup)] m-0 py-1 px-0 list-none bg-panel border border-solid border-line rounded-lg shadow-[0_8px_24px_color-mix(in_srgb,_var(--text)_12%,_transparent)] max-h-60 overflow-y-auto" aria-label="Time zones" preventScroll={false}>
      {#each matches as option (option.value)}
        <Combobox.Item value={option.value} label={option.label}>{option.label}</Combobox.Item>
      {/each}
      {#if !matches.length}<p class="small muted zone-empty text-ui-sm my-1.5 mx-3" role="status">No matching time zones.</p>{/if}
    </Combobox.ContentStatic>
  </div>
</Combobox.Root>
