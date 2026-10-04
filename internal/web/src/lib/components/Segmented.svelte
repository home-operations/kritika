<script lang="ts" generics="V extends string">
  // A choice of a few, all in view: Bits UI's radio group drawn as one
  // control. Arrow keys move the choice, as in a native radio group.
  import { RadioGroup } from 'bits-ui';
  let {
    label,
    options,
    value,
    onchange,
    disabled = false,
    path,
  }: {
    label: string;
    options: readonly { value: V; label: string }[];
    value: V;
    onchange: (v: V) => void;
    disabled?: boolean;
    // path is the spec path a form marks the control with.
    path?: string;
  } = $props();
</script>

<RadioGroup.Root class="segmented" orientation="horizontal" aria-label={label} data-path={path} {value} {disabled} onValueChange={(v) => onchange(v as V)}>
  {#each options as o (o.value)}
    <RadioGroup.Item value={o.value}>{o.label}</RadioGroup.Item>
  {/each}
</RadioGroup.Root>
