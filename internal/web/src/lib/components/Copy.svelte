<script lang="ts">
  import Icon from '../Icon.svelte';
  import Tooltip from './Tooltip.svelte';
  import { mdiContentCopy, mdiCheck } from '../icons';
  let { text, label = 'Copy' }: { text: string; label?: string } = $props();
  let done = $state(false);

  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(text);
      done = true;
      setTimeout(() => (done = false), 1500);
    } catch (err) {
      console.error('copy:', err);
    }
  }
</script>

<Tooltip text={label} class="btn btn-small border border-solid border-line bg-surface text-ink rounded-md cursor-pointer inline-flex items-center gap-1.5 py-0.5 px-2 text-ui-xs" onclick={copy} aria-label={label}>
  <Icon path={done ? mdiCheck : mdiContentCopy} size={13} />
  <span>{done ? 'Copied' : 'Copy'}</span>
</Tooltip>
