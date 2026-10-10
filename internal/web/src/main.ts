import { mount } from 'svelte';
import '@fontsource-variable/schibsted-grotesk'; // UI/prose sans (--font-sans), self-hosted
import '@fontsource-variable/red-hat-mono'; // code/identifier mono (--font-mono)
import './reset.css';
import './app.css';
import './pages.css';
import './admin.css';
import App from './App.svelte';
import { bootedFrom } from './lib/events.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('#app mount point missing');

// The dev server serves each module on its own, so only a build has an
// entry script to name.
if (import.meta.env.PROD) bootedFrom(import.meta.url);

export default mount(App, { target });
