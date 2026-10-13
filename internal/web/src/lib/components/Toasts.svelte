<script lang="ts">
  import { toasts, dismiss } from '../toast.svelte';
</script>

<div class="toasts fixed right-4 bottom-4 z-[var(--z-toast)] flex flex-col gap-2 max-w-[min(420px,_calc(100vw_-_32px))]" role="status" aria-live="polite">
  {#each toasts.list as t (t.id)}
    <div class="toast tone-{t.tone}">
      <span>{t.text}</span>
      <button class="btn btn-small border border-solid border-line bg-surface text-ink rounded-md cursor-pointer inline-flex items-center gap-1.5 py-0.5 px-2 text-ui-xs" aria-label="Dismiss" onclick={() => dismiss(t.id)}>×</button>
    </div>
  {/each}
</div>
