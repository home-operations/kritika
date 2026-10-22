// Route builders for DTOs that carry a repository full name rather than
// separate owner/repo fields, and the API paths of an account.
import { slugPath, type Route } from './routes';
import { splitRepo } from './format';
import { safeHref } from './markdown';

// pullKey names a pull request across the account's repositories.
export function pullKey(p: { repository: string; number: number }): string {
  return `${p.repository}#${p.number}`;
}

export function pullRoute(slug: string, p: { repository: string; number: number }): Extract<Route, { name: 'pull' }> {
  const n = splitRepo(p.repository);
  return { name: 'pull', slug, owner: n.owner, repo: n.repo, number: p.number };
}

export function repoRoute(slug: string, fullName: string): Route {
  const n = splitRepo(fullName);
  return { name: 'repo', slug, owner: n.owner, repo: n.repo };
}

// accountApi is the API path of the account slug.
export function accountApi(slug: string): string {
  return `/api/v1/accounts/${slugPath(slug)}`;
}

export function rerunPath(slug: string, p: { repository: string; number: number }): string {
  const n = splitRepo(p.repository);
  return `${accountApi(slug)}/pulls/${encodeURIComponent(n.owner)}/${encodeURIComponent(n.repo)}/${p.number}/rerun`;
}

// forgeOf is the forge of an account slug, "<forge>/<name>".
export function forgeOf(slug: string): string {
  const i = slug.indexOf('/');
  return i < 0 ? slug : slug.slice(0, i);
}

// forgeName is how the forge of an account slug is named in a label.
export function forgeName(slug: string): string {
  switch (forgeOf(slug)) {
    case 'forgejo':
      return 'Forgejo';
    case 'gitlab':
      return 'GitLab';
    case 'gitea':
      return 'Gitea';
  }
  return 'GitHub';
}

// repoPath is a repository's full name as two path segments, owner and
// name: a GitLab project's name holds the subgroups it is in.
export function repoPath(fullName: string): string {
  const n = splitRepo(fullName);
  return `${encodeURIComponent(n.owner)}/${encodeURIComponent(n.repo)}`;
}

// threadUrl links the thread of an inline comment on a pull request of an
// account of slug; none for a finding kritika did not post inline, or
// whose comment id it lacks. GitHub anchors a review comment's thread by
// its discussion id; Forgejo and Gitea anchor a code comment as any
// comment, and GitLab every note by its id.
export function threadUrl(slug: string, pullUrl: string, commentId: number | null): string | undefined {
  if (!commentId) return undefined;
  switch (forgeOf(slug)) {
    case 'github':
      return safeHref(`${pullUrl}#discussion_r${commentId}`);
    case 'gitlab':
      return safeHref(`${pullUrl}#note_${commentId}`);
  }
  return safeHref(`${pullUrl}#issuecomment-${commentId}`);
}

// commentUrl links a comment on a pull request's conversation of an
// account of slug.
export function commentUrl(slug: string, pullUrl: string, commentId: number | null): string | undefined {
  if (!commentId) return undefined;
  return safeHref(forgeOf(slug) === 'gitlab' ? `${pullUrl}#note_${commentId}` : `${pullUrl}#issuecomment-${commentId}`);
}

export function cancelPath(slug: string, reviewId: string): string {
  return `${accountApi(slug)}/reviews/${encodeURIComponent(reviewId)}/cancel`;
}

export function reindexPath(slug: string, fullName: string): string {
  const n = splitRepo(fullName);
  return `${accountApi(slug)}/repos/${encodeURIComponent(n.owner)}/${encodeURIComponent(n.repo)}/reindex`;
}

export function turnedOnPath(slug: string, fullName: string): string {
  const n = splitRepo(fullName);
  return `${accountApi(slug)}/repos/${encodeURIComponent(n.owner)}/${encodeURIComponent(n.repo)}/turned-on`;
}
