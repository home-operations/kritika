// A dashboard to click through without a Go backend: serves a build of the
// dashboard and answers the API from the same goldens the Playwright suite
// mocks with. The viewer is an admin of the golden account. `mise run
// ui-mock` builds into dist-mock/ (its own directory, so a test run rebuilding
// dist/ cannot pull the files out from under an open page). It serves this on
// port 5544 for every interface, so a phone or laptop on the LAN can open it.
// `mise watch ui-mock -r` rebuilds and restarts it on every source change.
// Writes are accepted and forgotten, except the viewer's settings.
import { createServer, type ServerResponse } from 'node:http';
import { existsSync, readFileSync, statSync } from 'node:fs';
import { extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import * as g from './golden.ts';

const PORT = 5544;
const dist = fileURLToPath(new URL('../dist-mock/', import.meta.url));
const me = { ...g.me, admin: true };
const table = [
  [/\/api\/v1\/me$/, me],
  [/\/audit$/, g.pageOf([g.auditEvent])],
  ...g.defaultApi(),
] as const;

// The entry script this build's manifest names: the page reloads itself
// when a stream opens naming another, so a rebuild reaches an open tab.
function builtEntry(): string {
  const manifest = JSON.parse(readFileSync(join(dist, '.vite/manifest.json'), 'utf8')) as Record<string, { file: string }>;
  return manifest['index.html']!.file;
}

const types: Record<string, string> = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml',
  '.json': 'application/json', '.woff2': 'font/woff2', '.woff': 'font/woff', '.png': 'image/png',
};
// Nothing is cached: a rebuild must show on the next reload.
const noCache = { 'Cache-Control': 'no-store', Pragma: 'no-cache', Expires: '0' };

function json(res: ServerResponse, status: number, body: unknown): void {
  const data = JSON.stringify(body);
  res.writeHead(status, { ...noCache, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) });
  res.end(data);
}

createServer((req, res) => {
  const url = new URL(req.url ?? '/', 'http://mock');
  const path = url.pathname;
  if (path === '/api/events') {
    res.writeHead(200, { ...noCache, 'Content-Type': 'text/event-stream', Connection: 'keep-alive' });
    res.write(g.resyncFrame(builtEntry()));
    const ping = setInterval(() => res.write(': ping\n\n'), 15_000);
    req.on('close', () => clearInterval(ping));
    return;
  }
  if (path === '/auth/providers') return json(res, 200, [{ name: 'github', type: 'github', displayName: 'GitHub' }]);
  if (path.startsWith('/auth/')) return json(res, 204, null);
  if (path.startsWith('/api/')) {
    if (req.method !== 'GET') {
      if (!path.endsWith('/me/settings')) return json(res, 202, g.accepted);
      let body = '';
      req.on('data', (chunk: Buffer) => { body += chunk; });
      req.on('end', () => {
        me.settings = { ...me.settings, ...(JSON.parse(body || '{}') as Partial<typeof me.settings>) };
        json(res, 200, me.settings);
      });
      return;
    }
    const hit = table.find(([re]) => re.test(path));
    if (!hit) return json(res, 404, g.golden('error'));
    const reply = hit[1];
    return json(res, 200, typeof reply === 'function' ? (reply as (u: URL) => unknown)(url) : reply);
  }
  // The router lives in the hash, so every page path is index.html.
  let file = join(dist, path === '/' ? 'index.html' : path);
  if (!existsSync(file) || statSync(file).isDirectory()) file = join(dist, 'index.html');
  if (!existsSync(file)) {
    res.writeHead(503, { ...noCache, 'Content-Type': 'text/plain', 'Retry-After': '2' });
    return res.end('Building the dashboard; reload in a moment.');
  }
  const body = readFileSync(file);
  res.writeHead(200, { ...noCache, 'Content-Type': types[extname(file)] ?? 'application/octet-stream', 'Content-Length': body.length });
  res.end(body);
}).listen(PORT, '0.0.0.0', () => {
  console.log(`Mock dashboard on http://localhost:${PORT}/ as ${me.user.displayName}, an admin of ${g.SLUG}`);
});
