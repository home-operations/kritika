<script lang="ts">
  // Badges for what GitHub says a repository is: a fork, archived, public
  // or internal. A private repository, the usual case, carries none.
  let { r }: { r: { archived: boolean; fork: boolean; visibility: string } } = $props();
</script>

{#if r.fork}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="A fork of another repository">fork</span>{/if}
{#if r.archived}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Archived on GitHub: read-only">archived</span>{/if}
{#if r.visibility === 'public'}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Public on GitHub: anyone can read its reviews and comments">public</span>{/if}
{#if r.visibility === 'internal'}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium" title="Internal on GitHub: every member of the enterprise can read it">internal</span>{/if}
