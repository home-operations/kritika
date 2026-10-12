// The dashboard's two scopes and their navigation destinations. A route that
// names an account is in that account's scope, whose tabs are its sections;
// any other is in the instance's, whose tabs are the pages about every
// account at once. No tab chooses an account: one is entered by the scope
// switcher or a link that names it. Rune-free, like routes.ts, so tests can
// import it.
import { analyticsFilter, usageFilter, type Route } from './routes';

export type Section = 'analytics' | 'usage' | 'pulls' | 'findings' | 'queue' | 'followups' | 'repos' | 'rules' | 'settings';

interface SectionDef {
  label: string;
  names: readonly Route['name'][];
  home: (slug: string) => Route;
  admin?: boolean;
}

export const SECTIONS: Record<Section, SectionDef> = {
  analytics: { label: 'Analytics', names: ['account'], home: (slug) => ({ name: 'account', slug }) },
  usage: { label: 'Usage & costs', names: ['usage'], home: (slug) => ({ name: 'usage', slug }) },
  pulls: { label: 'Pull requests', names: ['pulls', 'pull', 'review'], home: (slug) => ({ name: 'pulls', slug }) },
  findings: { label: 'Findings', names: ['findings'], home: (slug) => ({ name: 'findings', slug }) },
  queue: { label: 'Queue', names: ['queue'], home: (slug) => ({ name: 'queue', slug }) },
  followups: { label: 'Follow-ups', names: ['followups'], home: (slug) => ({ name: 'followups', slug }) },
  repos: { label: 'Repositories', names: ['repos', 'repo'], home: (slug) => ({ name: 'repos', slug }) },
  rules: { label: 'Rules', names: ['rules'], home: (slug) => ({ name: 'rules', slug }) },
  settings: { label: 'Audit log', names: ['admin'], home: (slug) => ({ name: 'admin', slug, section: 'audit' }), admin: true },
};

export const NAV_GROUPS: readonly { label: string; sections: readonly Section[]; before?: InstanceTab; after?: InstanceTab }[] = [
  { label: 'Operations', sections: ['queue', 'repos'], before: { label: 'Overview', route: { name: 'overview' } } },
  { label: 'Insights', sections: ['analytics', 'usage'] },
  { label: 'Activity', sections: ['pulls', 'followups', 'findings'] },
  { label: 'Configuration', sections: ['rules', 'settings'], after: { label: 'Instance settings', route: { name: 'console' }, admin: true } },
];

export function sectionOf(r: Route): Section | undefined {
  return NAV_GROUPS.flatMap((g) => g.sections).find((s) => SECTIONS[s].names.includes(r.name));
}

// Reporting dates travel between analytics and usage; repository and model
// selections stay on their own page and account.
export function reportingRoute(to: Route, from: Route): Route {
  if (from.name !== 'account' && from.name !== 'usage') return to;
  if (to.name === 'account') {
    const filter = analyticsFilter({ days: from.filter?.days });
    return { ...to, ...(filter ? { filter } : {}) };
  }
  if (to.name === 'usage') {
    const filter = usageFilter({ days: from.filter?.days });
    return { ...to, ...(filter ? { filter } : {}) };
  }
  return to;
}

// One of the instance's tabs; admin is one only an admin has, and several
// one that only says something new with more than one account.
export interface InstanceTab {
  label: string;
  route: Route;
  admin?: boolean;
  several?: boolean;
}

export const INSTANCE_TABS: readonly InstanceTab[] = [
  { label: 'Overview', route: { name: 'overview' } },
  { label: 'Queue', route: { name: 'instanceQueue' }, several: true },
  { label: 'Instance settings', route: { name: 'console' }, admin: true },
];

// scopeOf is the account a route is in the scope of, undefined for one in
// the instance's.
export function scopeOf(r: Route): string | undefined {
  return 'slug' in r ? r.slug : undefined;
}

// pageIn is the page r shows, in the account slug: the same list or
// settings page, and for a record, which the other account does not have,
// the list it belongs to. Account-specific filters stay behind; reporting
// dates, metrics and grouping carry across. The instance queue becomes the account's.
export function pageIn(r: Route, slug: string): Route {
  switch (r.name) {
    case 'account':
      return reportingRoute({ name: 'account', slug }, r);
    case 'usage': {
      const filter = usageFilter({ days: r.filter?.days, metric: r.filter?.metric, group: r.filter?.group });
      return { name: 'usage', slug, ...(filter ? { filter } : {}) };
    }
    case 'findings':
    case 'pulls':
    case 'queue':
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
    case 'instanceQueue':
      return { name: 'queue', slug };
    default:
      return { name: 'account', slug };
  }
}

// pageOfInstance is the instance's page for what r shows: its queue from
// an account's, when the instance has that tab, and its overview otherwise.
export function pageOfInstance(r: Route, queue: boolean): Route {
  if (!scopeOf(r)) return r.name === 'signin' ? { name: 'overview' } : r;
  return r.name === 'queue' && queue ? { name: 'instanceQueue' } : { name: 'overview' };
}
