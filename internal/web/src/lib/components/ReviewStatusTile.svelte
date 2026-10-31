<script lang="ts">
  // A review's status as an icon on a tinted tile and a word: color where
  // the status asks for attention, quiet where it does not.
  import type { ReviewStatus } from '../types';
  import { reviewTone } from '../format';
  import Icon from '../Icon.svelte';
  import { mdiCheck, mdiProgressClock, mdiAlertCircleOutline, mdiGaugeFull, mdiDebugStepOver, mdiSwapHorizontal, mdiCancel } from '../icons';

  let { status, title }: { status: ReviewStatus; title?: string } = $props();

  const icons: Record<ReviewStatus, string> = {
    running: mdiProgressClock,
    prepared: mdiProgressClock,
    completed: mdiCheck,
    superseded: mdiSwapHorizontal,
    skipped: mdiDebugStepOver,
    capped: mdiGaugeFull,
    failed: mdiAlertCircleOutline,
    canceled: mdiCancel,
  };
</script>

<span class="status tone-{reviewTone[status]}" {title}>
  <span class="status-tile inline-flex items-center justify-center w-4.5 h-4.5 rounded-sm text-[var(--tone,_var(--muted))] [background:color-mix(in_srgb,_var(--tone,_var(--muted))_14%,_transparent)]"><Icon path={icons[status]} size={12} /></span>
  <span class="status-word">{status}</span>
</span>
