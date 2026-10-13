<script lang="ts">
  import { href } from '../router.svelte';
  import { focusWhenShown } from '../focus';
  import { CONSOLE_SECTIONS } from '../settingsindex';
</script>

<nav class="subnav [flex:0_0_210px] flex flex-col gap-0.5 py-3.5 px-2.5 border-r border-solid border-r-line overflow-y-auto" aria-label="Settings sections">
  <p class="subnav-group mt-3 mx-2.5 mb-1 text-ui-sm font-[650] text-muted overflow-hidden text-ellipsis whitespace-nowrap">On this page</p>
  {#each CONSOLE_SECTIONS as p (p.target)}
    <a class="subnav-part block py-[3px] px-2 rounded-sm text-muted text-ui-sm whitespace-nowrap" href={href({ name: 'console' })} onclick={() => focusWhenShown(p.target, true)}>{p.label}</a>
  {/each}
</nav>
