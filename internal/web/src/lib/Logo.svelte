<script lang="ts">
  // The mark, drawn inline so it takes the theme's colors: a magnifying
  // glass over lines of text, one of them marked. During the brand's review
  // pass the glass tilts and settles while the lines redraw in turn, the
  // marked one last. public/favicon.svg is the same shape with less in the
  // lens, for the tab.
  let { size = 28 }: { size?: number } = $props();
</script>

<svg class="logo" viewBox="0 0 64 64" width={size} height={size} aria-hidden="true">
  <circle cx="27" cy="27" r="19" fill="var(--panel)" stroke="var(--accent)" stroke-width="5" />
  <rect class="line" style:--i={0} x="16" y="17.5" width="15" height="3" rx="1.5" fill="var(--text)" />
  <rect class="line" style:--i={1} x="16" y="24" width="22" height="3" rx="1.5" fill="var(--text)" />
  <rect class="line" style:--i={3} x="16" y="30.5" width="19" height="3" rx="1.5" fill="#f5df4d" />
  <rect class="line" style:--i={2} x="16" y="37" width="12" height="3" rx="1.5" fill="var(--muted)" />
  <path d="M41.5 41.5 56 56" stroke="var(--accent)" stroke-width="7" stroke-linecap="round" />
</svg>
