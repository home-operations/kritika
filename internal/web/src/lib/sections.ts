// The dashboard's two scopes and their navigation destinations. A route that
// names an account is in that account's scope, whose tabs are its sections;
// any other is in the instance's, whose tabs are the pages about every
// account at once. No tab chooses an account: one is entered by the scope
// switcher or a link that names it. Rune-free, like routes.ts, so tests can
// import it.
import { analyticsFilter, pullFilter, usageFilter, type Route } from './routes';

export type Section = 'overview' | 'analytics' | 'usage' | 'pulls' | 'findings' | 'queue' | 'followups' | 'repos' | 'rules' | 'settings';

interface SectionDef {
  label: string;
  names: readonly Route['name'][];
  home: (slug: string) => Route;
  admin?: boolean;
}

export const SECTIONS: Record<Section, SectionDef> = {
  overview: { label: 'Overview', names: ['account'], home: (slug) => ({ name: 'account', slug }) },
  analytics: { label: 'Analytics', names: ['analytics'], home: (slug) => ({ name: 'analytics', slug }) },
  usage: { label: 'Usage & costs', names: ['usage'], home: (slug) => ({ name: 'usage', slug }) },
  pulls: { label: 'Pull requests', names: ['pulls', 'pull', 'review'], home: (slug) => ({ name: 'pulls', slug }) },
  findings: { label: 'Findings', names: ['findings'], home: (slug) => ({ name: 'findings', slug }) },
  queue: { label: 'Queue', names: ['queue'], home: (slug) => ({ name: 'queue', slug }) },
  followups: { label: 'Follow-ups', names: ['followups'], home: (slug) => ({ name: 'followups', slug }) },
  repos: { label: 'Repositories', names: ['repos', 'repo'], home: (slug) => ({ name: 'repos', slug }) },
  rules: { label: 'Rules', names: ['rules'], home: (slug) => ({ name: 'rules', slug }) },
  settings: { label: 'Audit log', names: ['admin'], home: (slug) => ({ name: 'admin', slug, section: 'audit' }), admin: true },
};

export const NAV_GROUPS: readonly { label: string; sections: readonly Section[] }[] = [
  { label: 'Operations', sections: ['overview', 'pulls', 'queue', 'repos'] },
  { label: 'Insights', sections: ['analytics', 'usage'] },
  { label: 'Activity', sections: ['followups', 'findings'] },
  { label: 'Configuration', sections: ['rules', 'settings'] },
];

export function sectionOf(r: Route): Section | undefined {
  return NAV_GROUPS.flatMap((g) => g.sections).find((s) => SECTIONS[s].names.includes(r.name));
}

// Reporting dates travel between analytics and usage; repository and model
// selections stay on their own page and account.
export function reportingRoute(to: Route, from: Route): Route {
  if (from.name !== 'analytics' && from.name !== 'usage') return to;
  if (to.name === 'analytics') {
    const filter = analyticsFilter({ days: from.filter?.days });
    return { ...to, ...(filter ? { filter } : {}) };
  }
  if (to.name === 'usage') {
    const filter = usageFilter({ days: from.filter?.days });
    return { ...to, ...(filter ? { filter } : {}) };
  }
  return to;
}

// Global destinations available without choosing an account.
export interface InstanceTab {
  label: string;
  route: Route;
  admin?: boolean;
  // group is the sidebar heading it sits under, shared with NAV_GROUPS.
  group: string;
}

export const INSTANCE_TABS: readonly InstanceTab[] = [
  { label: 'Overview', route: { name: 'overview' }, group: 'Operations' },
  { label: 'Pull requests', route: { name: 'instancePulls' }, group: 'Operations' },
  { label: 'Queue', route: { name: 'instanceQueue' }, group: 'Operations' },
  { label: 'Analytics', route: { name: 'instanceAnalytics' }, group: 'Insights' },
  { label: 'Usage & costs', route: { name: 'instanceUsage' }, group: 'Insights' },
  { label: 'Settings', route: { name: 'console' }, admin: true, group: 'Configuration' },
];

// scopeOf is the account a route is in the scope of, undefined for one in
// the instance's.
export function scopeOf(r: Route): string | undefined {
  return 'slug' in r ? r.slug : undefined;
}

// pageIn is the page r shows, in the account slug: the same list or
// settings page, and for a record, which the other account does not have,
// the list it belongs to. Account-specific filters stay behind; reporting
// dates, metrics and grouping carry across. Portable pull and queue filters
// follow scope; a global repository query is retained when narrowing it.
export function pageIn(r: Route, slug: string): Route {
  switch (r.name) {
    case 'analytics':
      return reportingRoute({ name: 'analytics', slug }, r);
    case 'usage': {
      const filter = usageFilter({ days: r.filter?.days, metric: r.filter?.metric, group: r.filter?.group });
      return { name: 'usage', slug, ...(filter ? { filter } : {}) };
    }
    case 'findings':
    case 'followups':
    case 'rules':
    case 'repos':
      return { name: r.name, slug };
    case 'admin':
      return r.section ? { name: 'admin', slug, section: r.section } : { name: 'admin', slug };
    case 'pull':
    case 'review':
      return { name: 'pulls', slug };
    case 'repo':
      return { name: 'repos', slug };
    case 'instancePulls':
    case 'pulls': {
      const filter = r.name === 'pulls' && r.slug !== slug ? pullFilter({ ...r.filter, repo: undefined }) : r.filter;
      return { name: 'pulls', slug, ...(filter ? { filter } : {}) };
    }
    case 'instanceQueue':
    case 'queue':
      return { name: 'queue', slug, ...(r.filter ? { filter: r.filter } : {}) };
    case 'instanceAnalytics':
      return { name: 'analytics', slug, ...(r.filter ? { filter: r.filter } : {}) };
    case 'instanceUsage': {
      // By account is the instance's grouping; the period, metric and other groupings travel.
      const filter = usageFilter({ days: r.filter?.days, metric: r.filter?.metric, group: r.filter?.group });
      return { name: 'usage', slug, ...(filter ? { filter } : {}) };
    }
    default:
      return { name: 'account', slug };
  }
}

// pageOfInstance preserves the list and its filters when widening scope.
export function pageOfInstance(r: Route): Route {
  if (!scopeOf(r)) return r.name === 'signin' ? { name: 'overview' } : r;
  if (r.name === 'queue') return { name: 'instanceQueue', ...(r.filter ? { filter: r.filter } : {}) };
  if (r.name === 'pulls') return { name: 'instancePulls', ...(r.filter ? { filter: r.filter } : {}) };
  if (r.name === 'pull' || r.name === 'review') return { name: 'instancePulls' };
  if (r.name === 'analytics') return { name: 'instanceAnalytics', ...(r.filter ? { filter: r.filter } : {}) };
  if (r.name === 'usage') return { name: 'instanceUsage', ...(r.filter ? { filter: r.filter } : {}) };
  return { name: 'overview' };
}
