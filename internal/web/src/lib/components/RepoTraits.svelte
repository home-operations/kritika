<script lang="ts">
  // Badges for what GitHub says a repository is: a fork, archived, or both.
  let { r }: { r: { archived: boolean; fork: boolean } } = $props();
</script>

{#if r.fork}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="A fork of another repository">fork</span>{/if}
{#if r.archived}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Archived on GitHub: read-only">archived</span>{/if}
