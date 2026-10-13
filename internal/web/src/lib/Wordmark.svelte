<script lang="ts">
  // The name, which takes a review pass once the page has painted and on
  // hover: a cursor sweeps across it and each letter it reaches flashes and
  // lifts, then settles. A run always plays to its end, so leaving the brand
  // mid-way never snaps the letters back. Nothing moves for a reader who
  // asked the system for less motion.
  let { text = 'kritika' }: { text?: string } = $props();
  let playing = $state(false);
  const letters = $derived([...text]);

  $effect(() => {
    const t = setTimeout(play, 200);
    return () => clearTimeout(t);
  });

  export function play(): void {
    if (playing || matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    playing = true;
  }
</script>

<span class="wordmark inline-block whitespace-nowrap font-extrabold tracking-[-0.01em] text-[length:25px]" class:playing aria-hidden="true">
  {#each letters as ch, i (i)}
    <span class="letter inline-block" style:--i={i} onanimationend={() => { if (i === letters.length - 1) playing = false; }}>{ch}</span>
  {/each}
</span>
