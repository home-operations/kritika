<script lang="ts">
  import type { ComponentProps } from 'svelte';
  import type ColumnChartContent from './ColumnChartContent.svelte';

  let props: ComponentProps<typeof ColumnChartContent> = $props();
  const chart = import('./ColumnChartContent.svelte');
</script>

{#await chart}
  <div role="status" style:min-height="{props.height ?? 180}px">Loading chart…</div>
{:then { default: Chart }}
  <Chart {...props} />
{/await}
