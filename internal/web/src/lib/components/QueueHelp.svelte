<script lang="ts">
  import HelpPopover from './HelpPopover.svelte';
</script>

<HelpPopover label="About the queue">
  <p>Up to 500 active and 50 finished jobs per account, prioritizing recent failures. Refreshes every 15 seconds.</p>
  <p>Failed-job alerts cover the last 24 hours and clear when a replacement is queued, running or completed. An overdue heartbeat is a signal to investigate while the worker recovers.</p>
</HelpPopover>
