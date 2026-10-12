<script lang="ts">
  import { href } from '../router.svelte';
  import { focusWhenShown } from '../focus';
  import { CONSOLE_SECTIONS } from '../settingsindex';
</script>

<nav class="subnav" aria-label="Settings sections">
  <p class="subnav-group">On this page</p>
  {#each CONSOLE_SECTIONS as p (p.target)}
    <a class="subnav-part" href={href({ name: 'console' })} onclick={() => focusWhenShown(p.target, true)}>{p.label}</a>
  {/each}
</nav>
