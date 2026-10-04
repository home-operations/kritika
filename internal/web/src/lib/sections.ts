// The dashboard's two scopes and their tabs under the top bar. A route that
// names an account is in that account's scope, whose tabs are its sections;
// any other is in the instance's, whose tabs are the pages about every
// account at once. No tab chooses an account: one is entered by the scope
// switcher or a link that names it. Rune-free, like routes.ts, so tests can
// import it.
import type { Route } from './routes';

export type Section = 'analytics' | 'pulls' | 'rules' | 'settings';

type Name = Route['name'];

interface SectionDef {
  label: string;
  // names are the routes the section's tab is current on.
  names: readonly Name[];
  // home is where the section's tab goes.
  home: (slug: string) => Route;
}

export const SECTIONS: Record<Section, SectionDef> = {
  analytics: { label: 'Analytics', names: ['account', 'findings', 'usage'], home: (slug) => ({ name: 'account', slug }) },
  pulls: { label: 'Pull requests', names: ['pulls', 'pull', 'review', 'queue', 'followups'], home: (slug) => ({ name: 'pulls', slug }) },
  rules: { label: 'Rules', names: ['rules'], home: (slug) => ({ name: 'rules', slug }) },
  settings: { label: 'Settings', names: ['repos', 'repo', 'admin'], home: (slug) => ({ name: 'repos', slug }) },
};

export const SECTION_ORDER: readonly Section[] = ['analytics', 'pulls', 'rules', 'settings'];

export function sectionOf(r: Route): Section | undefined {
  return SECTION_ORDER.find((s) => SECTIONS[s].names.includes(r.name));
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
  { label: 'Configuration', route: { name: 'console' }, admin: true },
];

// scopeOf is the account a route is in the scope of, undefined for one in
// the instance's.
export function scopeOf(r: Route): string | undefined {
  return 'slug' in r ? r.slug : undefined;
}

// A section's list pages share one strip of sub-tabs in place of a page
// title; a record's own page (a pull, a review) keeps its title instead.
export interface SubTab {
  label: string;
  name: Name;
  route: (slug: string) => Route;
}

export const SUB_TABS: Partial<Record<Section, readonly SubTab[]>> = {
  analytics: [
    { label: 'Reviews', name: 'account', route: (slug) => ({ name: 'account', slug }) },
    { label: 'Findings', name: 'findings', route: (slug) => ({ name: 'findings', slug }) },
    { label: 'Spend', name: 'usage', route: (slug) => ({ name: 'usage', slug }) },
  ],
  pulls: [
    { label: 'Pull requests', name: 'pulls', route: (slug) => ({ name: 'pulls', slug }) },
    { label: 'Queue', name: 'queue', route: (slug) => ({ name: 'queue', slug }) },
    { label: 'Follow-ups', name: 'followups', route: (slug) => ({ name: 'followups', slug }) },
  ],
};
