// Sets the theme class before the first paint. The bundle is a module, and
// the browser can paint the light default before modules run. This is a file
// rather than an inline script, which the CSP (script-src 'self') refuses.
// It reads the preference that theme.svelte.ts stores.
(() => {
  let pref = 'auto';
  try {
    pref = localStorage.getItem('kritika-theme') ?? 'auto';
  } catch {
    // Storage blocked: follow the OS, as theme.svelte.ts does.
  }
  const dark = pref === 'dark' || (pref !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.classList.add(dark ? 'dark' : 'light');
})();
