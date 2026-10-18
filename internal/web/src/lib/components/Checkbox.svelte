<script lang="ts">
  // A checkbox on Bits UI's, for the one state a native input cannot show
  // without script: indeterminate, when some but not all of a set are
  // picked. Clicking a mixed box picks the whole set.
  import { Checkbox } from 'bits-ui';
  let {
    label,
    checked,
    indeterminate = false,
    disabled = false,
    onchange,
  }: { label: string; checked: boolean; indeterminate?: boolean; disabled?: boolean; onchange: (on: boolean) => void } = $props();
</script>

<Checkbox.Root class="checkbox" aria-label={label} {checked} {indeterminate} {disabled} onCheckedChange={onchange} />
