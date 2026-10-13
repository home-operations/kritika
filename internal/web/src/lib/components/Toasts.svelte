<script lang="ts">
  import { toasts, dismiss } from '../toast.svelte';
</script>

<div class="toasts fixed right-4 bottom-4 z-[var(--z-toast)] flex flex-col gap-2 max-w-[min(420px,_calc(100vw_-_32px))]" role="status" aria-live="polite">
  {#each toasts.list as t (t.id)}
    <div class="toast tone-{t.tone}">
      <span>{t.text}</span>
      <button class="btn btn-small" aria-label="Dismiss" onclick={() => dismiss(t.id)}>×</button>
    </div>
  {/each}
</div>
