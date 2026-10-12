import { tick } from 'svelte';

const scrolls = new Map<string, number>();
const sizes = new Map<string, number>();

function remember(map: Map<string, number>, key: string, value: number): void {
  map.delete(key);
  map.set(key, value);
  if (map.size > 100) map.delete(map.keys().next().value!);
}

export const loadedSize = (url: string): number => sizes.get(url) ?? 0;
export const rememberSize = (url: string, count: number): void => remember(sizes, url, count);

// Async lists grow after navigation. Restore against their rendered height,
// and stop trying as soon as the user starts scrolling or using the keyboard.
export function rememberScroll(key: string) {
  return (host: HTMLElement) => {
    if (!key) return;
    let disposed = false;
    let stop = () => {};
    void tick().then(() => {
      if (disposed) return;
      const page = host.querySelector<HTMLElement>('main.page');
      const content = page?.querySelector<HTMLElement>('.page-inner');
      if (!page || !content) return;
      const target = scrolls.get(key) ?? 0;
      let restoring = target > 0;
      let top = target;
      const restore = () => {
        if (!restoring) return;
        page.scrollTop = target;
        if (Math.abs(page.scrollTop - target) < 2) { restoring = false; observer.disconnect(); }
      };
      const observer = new ResizeObserver(restore);
      const cancel = () => { restoring = false; observer.disconnect(); top = page.scrollTop; };
      const scrolled = () => { if (!restoring) top = page.scrollTop; };
      page.scrollTop = 0;
      page.addEventListener('scroll', scrolled);
      for (const event of ['wheel', 'touchstart', 'keydown']) page.addEventListener(event, cancel, { passive: true });
      if (restoring) observer.observe(content);
      stop = () => {
        remember(scrolls, key, top);
        observer.disconnect();
        page.removeEventListener('scroll', scrolled);
        for (const event of ['wheel', 'touchstart', 'keydown']) page.removeEventListener(event, cancel);
      };
    });
    return () => { disposed = true; stop(); };
  };
}
