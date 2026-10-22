import type { Resource } from '../api/types';

/** Case-insensitive match on name or ID; a blank query keeps everything (CON-05). */
export function filterResources(items: readonly Resource[], query: string): Resource[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  return items.filter((i) => i.name.toLowerCase().includes(q) || i.id.toLowerCase().includes(q));
}
