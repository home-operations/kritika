<script lang="ts">
  import type { ToolCall } from '../../types';
  import { pretty } from '../../format';
  import CodeBlock from '../CodeBlock.svelte';
  let { call }: { call: ToolCall } = $props();
</script>

<div class="tool-call flex flex-col gap-1">
  <div class="tool-call-head flex items-center gap-2">
    <span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium">tool call</span>
    <span class="mono tool-name font-semibold">{call.name}</span>
    <span class="mono small muted text-ui-sm">{call.id}</span>
  </div>
  <CodeBlock text={pretty(call.input)} label="input" lang="json" />
</div>
