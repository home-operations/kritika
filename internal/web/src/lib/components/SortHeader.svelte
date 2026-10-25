<script lang="ts" generics="K extends string">
  // A column header that sorts its table: a button in the th, whose
  // aria-sort says the column and direction the rows are in.
  import Icon from '../Icon.svelte';
  import { mdiArrowDown, mdiArrowUp, mdiSwapVertical } from '../icons';
  import type { SortControl } from '../sort.svelte';

  let { label, key, sorter, class: klass }: { label: string; key: K; sorter: SortControl<K>; class?: string } = $props();
  const state = $derived(sorter.aria(key));
</script>

<th scope="col" class={klass} aria-sort={state}>
  <button type="button" class="sort-button" onclick={() => sorter.toggle(key)}>
    <span>{label}</span>
    <span class="sort-icon" class:sort-active={state !== 'none'}>
      <Icon path={state === 'ascending' ? mdiArrowUp : state === 'descending' ? mdiArrowDown : mdiSwapVertical} size={12} />
    </span>
  </button>
</th>
