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
  <div class="search-combo zone-picker">
    <Combobox.Input
      id="pref-zone"
      autocomplete="off"
      placeholder="Search time zones"
      onclick={() => (open = true)}
      aria-controls={open ? 'pref-zone-options' : undefined}
      onfocus={() => (open = true)}
      oninput={(e) => { open = true; search = e.currentTarget.value; }}
    />
    <Combobox.ContentStatic id="pref-zone-options" class="suggest zone-options" aria-label="Time zones" preventScroll={false}>
      {#each matches as option (option.value)}
        <Combobox.Item value={option.value} label={option.label}>{option.label}</Combobox.Item>
      {/each}
      {#if !matches.length}<p class="small muted zone-empty" role="status">No matching time zones.</p>{/if}
    </Combobox.ContentStatic>
  </div>
</Combobox.Root>
