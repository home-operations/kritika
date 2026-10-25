import { test, expect } from './fixtures';
import * as g from './golden';
import type {
  IndexChunks,
  IndexContents,
  IndexFile,
  IndexJob,
  Job,
  RepoDetail,
  Repository,
} from '../src/lib/types';

const account = `/#/a/${g.SLUG}`;
const api = `/api/v1/accounts/${g.SLUG}`;
const admin = { ...g.me, admin: true };
const base = g.repoPage.items[0]!;
const queued: IndexJob = {
  id: 42,
  state: 'available',
  trigger: 'reindex',
  full: true,
  attempt: 0,
  createdAt: '2026-09-01T12:00:00Z',
  attemptedAt: null,
};
const repo = (fullName: string, index: Partial<Repository['index']>): Repository => ({
  ...base,
  id: `id-${fullName}`,
  fullName,
  index: { ...base.index, job: null, ...index },
});

test.beforeEach(async ({ page }) => {
  page.on('pageerror', (err) => {
    throw err;
  });
});

test('repository columns sort either way, and say so', async ({ page }) => {
  const rows = [
    repo('alpha/mid', { lastRunStatus: 'completed' }),
    repo('alpha/first', { lastRunStatus: '', lastRunAt: null, backlog: 2 }),
    repo('alpha/last', { job: { ...queued, state: 'running', trigger: 'onboard', full: false } }),
  ];
  await g.mockApi(page, [[new RegExp(`${api}/repos$`), g.pageOf(rows)], ...g.defaultApi()]);
  await page.goto(`${account}/repos`);
  const names = page.locator('tbody td.mono a');
  await expect(names).toHaveText(['alpha/mid', 'alpha/first', 'alpha/last']);

  const header = page.getByRole('columnheader', { name: 'Repository', exact: true });
  await expect(header).toHaveAttribute('aria-sort', 'none');
  await header.getByRole('button').click();
  await expect(header).toHaveAttribute('aria-sort', 'ascending');
  await expect(names).toHaveText(['alpha/first', 'alpha/last', 'alpha/mid']);
  await header.getByRole('button').click();
  await expect(header).toHaveAttribute('aria-sort', 'descending');
  await expect(names).toHaveText(['alpha/mid', 'alpha/last', 'alpha/first']);

  // The Index column puts what is running first, then what waits.
  const index = page.getByRole('columnheader', { name: 'Index', exact: true });
  await index.getByRole('button').click();
  await expect(header).toHaveAttribute('aria-sort', 'none');
  await expect(names).toHaveText(['alpha/last', 'alpha/first', 'alpha/mid']);
  await expect(page.getByRole('row', { name: /alpha\/last/ })).toContainText('First index running');
  await expect(page.getByRole('row', { name: /alpha\/first/ })).toContainText('Waiting · #2');
  await expect(page.getByRole('row', { name: /alpha\/first/ })).not.toContainText('never');
});

test('the queue narrows to index jobs or to runs, in the URL', async ({ page }) => {
  const jobs: Job[] = [
    {
      ...g.job,
      id: 7,
      kind: 'index',
      state: 'available',
      args: { ...g.job.args, number: 0, trigger: 'onboard' },
    },
    { ...g.job, id: 8, kind: 'review', state: 'available' },
    { ...g.job, id: 9, kind: 'followup', state: 'running' },
  ];
  await g.mockApi(page, [[new RegExp(`${api}/queue$`), jobs], ...g.defaultApi()]);
  await page.goto(`${account}/queue`);
  const kinds = page.locator('tbody tr strong');
  await expect(kinds).toHaveText(['#7 · index', '#8 · review', '#9 · followup']);
  await page.getByRole('combobox', { name: 'Job kind' }).selectOption('index');
  await expect(kinds).toHaveText(['#7 · index']);
  await expect(page).toHaveURL(/kind=index/);
  await expect(page.getByRole('radio', { name: /^All/ })).toHaveAccessibleName('All (1)');
  await page.getByRole('combobox', { name: 'Job kind' }).selectOption('runs');
  await expect(kinds).toHaveText(['#8 · review', '#9 · followup']);
  await page.goto(`${account}/queue?kind=index`);
  await expect(kinds).toHaveText(['#7 · index']);
});

test('reindex shows the job it queued, and an already queued one is no failure', async ({
  page,
}) => {
  let detail: RepoDetail = {
    ...g.repoDetail,
    index: { ...g.repoDetail.index, job: null },
    indexJobs: [],
  };
  await g.mockApi(page, [
    [/\/api\/v1\/me$/, admin],
    [new RegExp(`${api}/repos/alpha/one$`), () => detail],
    ...g.defaultApi(),
  ]);
  let status = 202;
  await g.mockWrites(page, [
    [
      'POST',
      new RegExp(`${api}/repos/alpha/one/reindex$`),
      () => {
        if (status !== 202)
          return g.apiError(
            409,
            'already_queued',
            'a reindex is already queued for this repository',
          );
        detail = { ...detail, index: { ...detail.index, job: queued }, indexJobs: [queued] };
        return { status, body: { jobId: queued.id } };
      },
    ],
  ]);
  await page.goto(`${account}/repos/alpha/one`);
  await page.getByRole('button', { name: 'Reindex', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Reindex', exact: true }).click();
  await expect(page.getByText('Reindex queued, job #42')).toBeVisible();
  const queue = page.getByRole('region', { name: 'Index queue' });
  await expect(queue).toContainText('Reindex queued');
  await expect(queue).toContainText('job #42');
  await expect(queue.getByRole('link', { name: 'See the index queue' })).toHaveAttribute(
    'href',
    /queue\?kind=index$/,
  );
  await expect(page.getByRole('button', { name: 'Reindex queued' })).toBeDisabled();

  // Another tab queued one meanwhile: the server says so, and that is news, not an error.
  detail = { ...detail, index: { ...detail.index, job: null }, indexJobs: [] };
  status = 409;
  await page.reload();
  await page.getByRole('button', { name: 'Reindex', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Reindex', exact: true }).click();
  await expect(page.getByText('A reindex of this repository is already queued')).toBeVisible();
  await expect(page.getByText(/Reindex failed/)).toHaveCount(0);
});

test('a repository waiting for onboarding says where it is in line', async ({ page }) => {
  const detail: RepoDetail = {
    ...g.repoDetail,
    index: { ...g.repoDetail.index, job: null, backlog: 3 },
    indexJobs: [],
  };
  await g.mockApi(page, [[new RegExp(`${api}/repos/alpha/one$`), detail], ...g.defaultApi()]);
  await page.goto(`${account}/repos/alpha/one`);
  const queue = page.getByRole('region', { name: 'Index queue' });
  await expect(queue).toContainText('Waiting · #3');
  await expect(queue).toContainText('number 3 in line');
});

test("a repository's index lists its files and their chunks, the text for an admin", async ({
  page,
}) => {
  const contents: IndexContents = {
    indexed: true,
    commit: 'def4567890',
    embedModel: 'embed-small',
    embedDims: 1024,
    builtAt: '2026-09-01T12:00:00Z',
    finishedAt: '2026-09-01T12:01:30Z',
    chunks: 5,
    files: 2,
    current: true,
  };
  const files: IndexFile[] = [
    { path: 'cmd/app/main.go', language: 'go', chunks: 3 },
    { path: 'docs/readme.md', language: 'markdown', chunks: 2 },
  ];
  const chunks: IndexChunks = {
    path: 'cmd/app/main.go',
    truncated: false,
    items: [
      {
        startLine: 1,
        endLine: 12,
        language: 'go',
        symbol: 'main',
        kind: 'function',
        scope: '',
        text: 'func main() {}',
      },
    ],
  };
  const seen = await g.mockApi(page, [
    [/\/api\/v1\/me$/, admin],
    [new RegExp(`${api}/repos/alpha/one/index$`), contents],
    [
      new RegExp(`${api}/repos/alpha/one/index/files$`),
      (u: URL) => g.pageOf(files.filter((f) => f.path.includes(u.searchParams.get('q') ?? ''))),
    ],
    [new RegExp(`${api}/repos/alpha/one/index/chunks$`), chunks],
    ...g.defaultApi(),
  ]);
  await page.goto(`${account}/repos/alpha/one`);
  await page.getByRole('button', { name: 'Index contents', exact: true }).click();
  await expect(page.locator('dt').filter({ hasText: 'Chunks' }).locator('+ dd')).toHaveText('5');
  await expect(page.getByRole('cell', { name: 'docs/readme.md', exact: true })).toBeVisible();
  await page.getByPlaceholder('Filter by path').fill('cmd');
  await expect(page.getByRole('cell', { name: 'docs/readme.md', exact: true })).toHaveCount(0);
  expect(
    seen.some((u) => u.pathname.endsWith('/index/files') && u.searchParams.get('q') === 'cmd'),
  ).toBe(true);
  await page.getByRole('button', { name: 'Chunks of cmd/app/main.go' }).click();
  const region = page.getByRole('region', { name: 'Chunks of cmd/app/main.go' });
  await expect(region).toContainText('lines 1–12');
  await expect(region).toContainText('function main');
  await expect(region.locator('pre')).toHaveText('func main() {}');
});
