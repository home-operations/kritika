<script lang="ts">
  // The name, which can scramble: its letters run through random glyphs
  // and settle back into place left to right. The width is held while it
  // runs so the layout around it does not move. Nothing runs for a reader
  // who asked the system for less motion.
  let { text = 'kritika' }: { text?: string } = $props();
  // scrambled is the text while it runs; otherwise the name shows as given.
  let scrambled = $state<string>();
  let el = $state<HTMLSpanElement>();
  let width = $state<number>();
  let frame: number | undefined;
  const GLYPHS = 'abcdefghijklmnopqrstuvwxyz';
  const DURATION = 700;
  const FRAME = 45;

  export function scramble(): void {
    if (frame !== undefined || matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    width = el?.offsetWidth;
    const start = performance.now();
    let last = 0;
    const tick = (now: number) => {
      const t = Math.min(1, (now - start) / DURATION);
      if (now - last >= FRAME) {
        last = now;
        scrambled = [...text].map((ch, i) => (t >= (i + 1) / text.length ? ch : GLYPHS[Math.floor(Math.random() * GLYPHS.length)])).join('');
      }
      if (t < 1) {
        frame = requestAnimationFrame(tick);
      } else {
        scrambled = undefined;
        width = undefined;
        frame = undefined;
      }
    };
    frame = requestAnimationFrame(tick);
  }
</script>

<span bind:this={el} class="wordmark inline-block whitespace-nowrap font-extrabold tracking-[-0.01em] text-[length:25px]" style:width={width === undefined ? undefined : `${width}px`} aria-hidden="true">{scrambled ?? text}</span>
