import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // dist/.vite/manifest.json names the entry script, which the server
    // tells every event stream so a tab running another build reloads.
    manifest: true,
  },
});
