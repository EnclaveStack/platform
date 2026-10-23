import type { FinderItem } from '@enclavestack/ui';
import type { Resource } from '../api/types';
import { phaseLabel } from '../components/StatusBadge';
import { NAV_PAGES, SECTIONS, sectionByKind } from './sections';

const LIMIT = 12;

/** The pages Find knows, in the order they're offered. Settings follow what the person's role allows. */
function pages(roles: { peopleAdmin: boolean; machineAdmin: boolean }): FinderItem[] {
  const sections = SECTIONS.filter((s) => s.slug !== 'records');
  return [
    { id: '/', label: 'Overview', meta: 'Page' },
    ...sections.map((s) => ({ id: `/${s.slug}`, label: s.label, meta: 'Page' })),
    { id: NAV_PAGES.machines.to, label: NAV_PAGES.machines.label, meta: 'Page' },
    { id: NAV_PAGES.catalogue.to, label: NAV_PAGES.catalogue.label, meta: 'Page' },
    ...sections.map((s) => ({ id: `/guide/${s.slug}`, label: `${s.label} guide`, meta: 'Guide' })),
    { id: '/settings/account', label: 'Your account', meta: 'Settings' },
    { id: '/settings/tokens', label: 'API tokens', meta: 'Settings' },
    ...(roles.peopleAdmin ? [{ id: '/settings/people', label: 'People', meta: 'Settings' }] : []),
    ...(roles.machineAdmin ? [{ id: '/settings/projects', label: 'Projects', meta: 'Settings' }, { id: '/settings/audit', label: 'Audit log', meta: 'Settings' }] : []),
  ];
}

/**
 * What Find shows for `query` (CON-62): pages and guides whose name contains it first, then resources by
 * name with their kind and state. Each item's id is where it leads. Nothing until something is typed.
 */
export function findItems(query: string, resources: Resource[], roles: { peopleAdmin: boolean; machineAdmin: boolean }): FinderItem[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const hits = pages(roles).filter((p) => p.label.toLowerCase().includes(q));
  const found = resources
    .filter((r) => r.status.phase !== 'deleting' && r.name.toLowerCase().includes(q))
    .sort((a, b) => Number(!a.name.toLowerCase().startsWith(q)) - Number(!b.name.toLowerCase().startsWith(q)) || a.name.localeCompare(b.name))
    .map((r) => {
      const s = sectionByKind(r.kind);
      return { id: `/${s.slug}/${encodeURIComponent(r.id)}`, label: r.name, meta: `${s.noun} · ${phaseLabel(r.status.phase).toLowerCase()}` };
    });
  return [...hits, ...found].slice(0, LIMIT);
}
