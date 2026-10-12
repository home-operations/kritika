import { tick } from 'svelte';

const scrolls = new Map<string, number>();
const sizes = new Map<string, number>();
const mounted = new WeakMap<HTMLElement, string>();

function remember(map: Map<string, number>, key: string, value: number): void {
  map.delete(key);
  map.set(key, value);
  if (map.size > 100) map.delete(map.keys().next().value!);
}

export const loadedSize = (entry: string, url: string): number => sizes.get(`${entry}:${url}`) ?? 0;
export const rememberSize = (entry: string, url: string, count: number): void => remember(sizes, `${entry}:${url}`, count);

// Restore until the saved offset is reached or the initial data settles.
// A shorter result clamps once, so later polls and expansions cannot jump.
export function rememberScroll(key: string, entry: string, traversal: boolean) {
  return (host: HTMLElement) => {
    const samePage = mounted.get(host) === key;
    mounted.set(host, key);
    if (!key) return;
    let disposed = false;
    let stop = () => {};
    void tick().then(() => {
      if (disposed) return;
      const page = host.querySelector<HTMLElement>('main.page');
      const content = page?.querySelector<HTMLElement>('.page-inner');
      if (!page || !content) return;
      const target = traversal ? scrolls.get(entry) ?? 0 : samePage ? page.scrollTop : 0;
      let restoring = target > 0 && traversal;
      let top = target;
      const disconnect = () => { resize.disconnect(); mutation.disconnect(); };
      const cancel = () => { restoring = false; disconnect(); top = page.scrollTop; };
      const restore = () => {
        if (!restoring) return;
        page.scrollTop = target;
        if (Math.abs(page.scrollTop - target) < 2 || !content.querySelector('[data-loading="true"]')) cancel();
      };
      const resize = new ResizeObserver(restore);
      const mutation = new MutationObserver(restore);
      const scrolled = () => { if (!restoring) top = page.scrollTop; };
      page.scrollTop = restoring ? 0 : target;
      page.addEventListener('scroll', scrolled);
      for (const event of ['wheel', 'touchstart', 'keydown', 'pointerdown']) page.addEventListener(event, cancel, { passive: true });
      if (restoring) {
        resize.observe(content);
        mutation.observe(content, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-loading'] });
        restore();
      }
      stop = () => {
        remember(scrolls, entry, top);
        disconnect();
        page.removeEventListener('scroll', scrolled);
        for (const event of ['wheel', 'touchstart', 'keydown', 'pointerdown']) page.removeEventListener(event, cancel);
      };
    });
    return () => { disposed = true; stop(); };
  };
}
